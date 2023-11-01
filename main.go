package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/xanzy/go-gitlab"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type MrDeployStatus int64

const (
	NotDeployed MrDeployStatus = iota
	UpToDate
	UpdateAvailable
	Pending
	Desynchronized
)

type AppConfig struct {
	repoPid              string
	repoToken            string
	repoGitUrl           string
	repoApiUrl           string
	repoStagingBranch    string
	repoProductionBranch string

	registryPid        string
	registryMr         string
	registryStaging    string
	registryProduction string
}

type App struct {
	config    *AppConfig
	gitlab    *gitlab.Client
	k8sClient *kubernetes.Clientset

	mrCommits     map[string]bool
	prodCommits   map[string]bool
	stagingCommit map[string]bool
}

func NewApp(gitlabApi *gitlab.Client, k8sClient *kubernetes.Clientset, config *AppConfig) *App {
	return &App{
		config:    config,
		gitlab:    gitlabApi,
		k8sClient: k8sClient,

		mrCommits:     map[string]bool{},
		prodCommits:   map[string]bool{},
		stagingCommit: map[string]bool{},
	}
}

func (app *App) build(ctx string, dockerfile string, imageName string, imageTag string, env string) error {
	// Spawn a new pod to build the image
	log.Printf("Image tag for kaniko : %s", imageName+":"+imageTag)

	podName := "img-build-" + env

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "flux-system",
			Labels: map[string]string{
				"imag-build": "esap",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            podName,
					Image:           "gcr.io/kaniko-project/executor:v1.16.0",
					ImagePullPolicy: v1.PullIfNotPresent,
					Command: []string{
						"/kaniko/executor",
						"-c", ctx,
						"--context-sub-path", "esap",
						"-f", dockerfile,
						"-d", imageName + ":" + imageTag,
					},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "registry-config",
							ReadOnly:  true,
							MountPath: "/kaniko/.docker/",
						},
					},
				},
			},
			RestartPolicy: v1.RestartPolicyNever,
			Volumes: []v1.Volume{
				{
					Name: "registry-config",
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: "esap-image-deploy-secret",
							Items: []v1.KeyToPath{
								{
									Key:  ".dockerconfigjson",
									Path: "config.json",
								},
							},
						},
					},
				},
			},
		},
	}

	createdPod, err := app.k8sClient.CoreV1().Pods(pod.Namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	log.Printf("Pod created %s: %s", podName, createdPod)
	if err != nil {
		log.Printf("Error while creating k8s pod: %s", err)
	}
	return err
}

func (app *App) prepareContext(branch string, commit string) (string, error) {
	// clone env at provided commit
	gitUrl := strings.Replace(app.config.repoGitUrl, "https://", "git://oauth2:"+app.config.repoToken+"@", 1)

	url := gitUrl + "#refs/heads/" + branch
	if commit != "" {
		fmt.Printf("Prepare context : %s", strings.Replace(app.config.repoGitUrl, "https://", "git://oauth2:"+"TOKEN"+"@", 1)+"#refs/heads/"+branch+"#"+commit)
		url = url + "#" + commit
	} else {
		fmt.Printf("Prepare context : %s", strings.Replace(app.config.repoGitUrl, "https://", "git://oauth2:"+"TOKEN"+"@", 1)+"#refs/heads/"+branch)
	}

	return url, nil
}

func (app *App) loopMr() {
	openedState := "opened"
	openMergeRequests, _, err := app.gitlab.MergeRequests.ListProjectMergeRequests(app.config.repoPid, &gitlab.ListProjectMergeRequestsOptions{
		TargetBranch: &app.config.repoStagingBranch,
		State:        &openedState,
	})

	if err != nil {
		log.Printf("Unable to load MR")
		return
	}

	for _, mergeRequest := range openMergeRequests {
		commits, _, err := app.gitlab.MergeRequests.GetMergeRequestCommits(app.config.repoPid, mergeRequest.IID, &gitlab.GetMergeRequestCommitsOptions{PerPage: 1})
		// Latest commit
		if err != nil || len(commits) != 1 {
			log.Printf("No commit for MR %d - %s", mergeRequest.IID, err)
			continue
		}
		latestCommit := commits[0]

		if _, ok := app.mrCommits[latestCommit.ID]; !ok {
			// Prepare environment
			context, err := app.prepareContext(mergeRequest.SourceBranch, latestCommit.ID)
			if err != nil {
				log.Printf("Error while cloning MR environement: %s", err)
				continue
			}

			// Build image
			versionId := strconv.Itoa(int(latestCommit.CommittedDate.Unix()))
			err = app.build(context, "Dockerfile", app.config.registryMr+strconv.Itoa(mergeRequest.IID), versionId,
				"mr-"+strconv.Itoa(mergeRequest.IID)+"-"+latestCommit.ID)
			app.mrCommits[latestCommit.ID] = true

			if err != nil {
				log.Printf("Error while building MR image %d: %s", mergeRequest.IID, err)
			}
		}
	}
}

func (app *App) cleanMrRegistry() {
	openedState := "opened"
	openMergeRequests, _, err := app.gitlab.MergeRequests.ListProjectMergeRequests(app.config.repoPid, &gitlab.ListProjectMergeRequestsOptions{
		TargetBranch: &app.config.repoStagingBranch,
		State:        &openedState,
	})
	if err != nil {
		log.Printf("Unable to retrieve informations MR: %s", err)
		return
	}

	indexedMergeRequests := map[int]string{}
	for _, mergeRequest := range openMergeRequests {
		commits, _, err := app.gitlab.MergeRequests.GetMergeRequestCommits(app.config.repoPid, mergeRequest.IID, &gitlab.GetMergeRequestCommitsOptions{PerPage: 1})

		if err != nil || len(commits) == 0 {
			log.Printf("No commit for MR %d - %s", mergeRequest.IID, err)
			indexedMergeRequests[mergeRequest.IID] = ""
		} else {
			latestCommit := commits[0]
			versionId := strconv.Itoa(int(latestCommit.CommittedDate.Unix()))
			indexedMergeRequests[mergeRequest.IID] = versionId
		}
	}

	registries, _, err := app.gitlab.ContainerRegistry.ListProjectRegistryRepositories(app.config.registryPid, &gitlab.ListRegistryRepositoriesOptions{})
	if err != nil {
		log.Printf("Unable to retrieve Registry informations: %s", err)
		return
	}

	log.Printf("Existing registries: %v", registries)
	for _, registry := range registries {
		// Check if has right prefix
		prefix := "esap-mr-"
		if strings.HasPrefix(registry.Name, prefix) {
			// Check if registry fit open MR
			id := registry.Name[len(prefix):]
			log.Printf("Identified id for registry: %s from %s", id, registry.Name)
			intId, err := strconv.Atoi(id)
			if err == nil {
				if latestCommit, ok := indexedMergeRequests[intId]; ok {
					// Load tags
					tags, _, err := app.gitlab.ContainerRegistry.ListRegistryRepositoryTags(app.config.registryPid, registry.ID, &gitlab.ListRegistryRepositoryTagsOptions{})
					if err != nil {
						log.Printf("Unable to load registry tags : %s", err)
						continue
					}

					if len(tags) <= 1 {
						continue
					}

					indexedtags := map[string]bool{}
					for _, tag := range tags {
						indexedtags[tag.Name] = true
					}

					log.Printf("Identified tags (%s): %v", latestCommit, tags)
					// Identify latest build tag for MR
					if _, ok := indexedtags[latestCommit]; ok {
						log.Printf("Remove partial registry: %s", registry.Name)
						// Remove all other tags
						for _, tag := range tags {
							if tag.Name != latestCommit {
								log.Printf("Remove registry tag %s:%s (identified latest: %s)", registry.Name, tag.Name, latestCommit)
								_, err := app.gitlab.ContainerRegistry.DeleteRegistryRepositoryTag(app.config.registryPid, registry.ID, tag.Name)
								if err != nil {
									log.Printf("Unable to delete registry tag %s: %s", tag.Name, err)
								}
							}
						}
					}
				} else {
					log.Printf("Remove full registry : %s", registry.Name)
					_, err := app.gitlab.ContainerRegistry.DeleteRegistryRepository(app.config.registryPid, registry.ID)
					if err != nil {
						log.Printf("Unable to drop registry %s: %s", registry.Name, err)
					}
				}
			}
		}
	}
}

func (app *App) loopStaging() {
	// Load latest commit
	branche, _, err := app.gitlab.Branches.GetBranch(app.config.repoPid, app.config.repoStagingBranch)
	if err != nil {
		log.Printf("Staging Loop: Unable to load branch %s: %s", app.config.repoStagingBranch, err)
		return
	}

	if _, ok := app.stagingCommit[branche.Commit.ID]; !ok {
		// Prepare environment
		context, err := app.prepareContext(app.config.repoStagingBranch, branche.Commit.ID)
		if err != nil {
			log.Printf("Error while cloning staging environement: %s", err)
			return
		}

		// Build image
		versionId := strconv.Itoa(int(branche.Commit.CommittedDate.Unix()))
		err = app.build(context, "Dockerfile", app.config.repoStagingBranch, versionId, "staging-"+versionId)
		app.stagingCommit[versionId] = true

		if err != nil {
			log.Printf("Error while building staging image '%s': %s", versionId, err)
		}
	}
}

func (app *App) cleanStagingRegistry() {
	registries, _, err := app.gitlab.ContainerRegistry.ListProjectRegistryRepositories(app.config.registryPid, &gitlab.ListRegistryRepositoriesOptions{})
	if err != nil {
		log.Printf("Unable to retrieve Registry informations: %s", err)
		return
	}

	registryId := -1
	splits := strings.Split(app.config.registryStaging, "/")
	registryName := splits[len(splits)-1]

	for _, registry := range registries {
		if registry.Name == registryName {
			registryId = registry.ID
			break
		}
	}
	if registryId == -1 {
		log.Printf("No staging registry identified for cleaning")
		return
	}

	// Load latest commit
	branche, _, err := app.gitlab.Branches.GetBranch(app.config.repoPid, app.config.repoStagingBranch)
	if err != nil {
		log.Printf("Staging cleaning: Unable to load branch %s : %s", app.config.repoStagingBranch, err)
		return
	}
	latestCommit := strconv.Itoa(int(branche.Commit.CommittedDate.Unix()))

	// Load tags
	tags, _, err := app.gitlab.ContainerRegistry.ListRegistryRepositoryTags(app.config.registryPid, registryId, &gitlab.ListRegistryRepositoryTagsOptions{})
	if err != nil {
		log.Printf("Unable to load registry tags : %s", err)
		return
	}

	if len(tags) <= 1 {
		return
	}
	log.Printf("Identified tags (%s): %v", branche.Commit.ID, tags)

	// Remove old tags
	for _, tag := range tags {
		if tag.Name != latestCommit {
			log.Printf("Remove registry tag %s:%s (identified latest: %s)", registryName, tag.Name, latestCommit)
			_, err := app.gitlab.ContainerRegistry.DeleteRegistryRepositoryTag(app.config.registryPid, registryId, tag.Name)
			if err != nil {
				log.Printf("Unable to delete registry tag %s: %s", tag.Name, err)
			}
		}
	}
}

func (app *App) loopProduction() {
	// Load latest tag
	tags, _, err := app.gitlab.Tags.ListTags(app.config.repoPid, &gitlab.ListTagsOptions{})
	if err != nil {
		return
	}

	var latestTag string = ""
	var latestTagCommit string = ""
	var latestParsedTag *semver.Version = nil

	for _, tag := range tags {
		// Check semver version and compare to latest tag
		t, err := semver.NewVersion(tag.Name)

		if err == nil && (latestParsedTag == nil || t.GreaterThan(latestParsedTag)) {
			latestTag = tag.Name
			latestParsedTag = t
			latestTagCommit = tag.Commit.ID
		}
	}

	if latestTag == "" {
		log.Printf("Unable to locate any valid production tag")
		return
	}

	if _, ok := app.prodCommits[latestTag]; !ok {
		// Prepare environment
		context, err := app.prepareContext(app.config.repoProductionBranch, latestTagCommit)
		if err != nil {
			log.Printf("Error while cloning production environement: %s", err)
			return
		}

		// Build image
		err = app.build(context, "Dockerfile", app.config.registryProduction, latestTag, "prod-"+latestTag)
		app.prodCommits[latestTag] = true

		if err != nil {
			log.Printf("Error while building prod image '%s': %s", latestTagCommit, err)
		}
	}
}

func (app *App) loop() {
	app.loopProduction()
	app.loopStaging()
	app.loopMr()

	app.cleanMrRegistry()
	app.cleanStagingRegistry()

	// TODO: Notify gitlab with crashed builds
}

func (app *App) Run() {
	ticker := time.NewTicker(2 * time.Minute)
	quit := make(chan bool)
	go func() {
		app.loop()
		for {
			log.Println("Loop start")
			select {
			case <-ticker.C:
				app.loop()
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()

	interruptChan := make(chan os.Signal, 1)
	signal.Notify(interruptChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive our signal.
	<-interruptChan

	// create a deadline to wait for.
	_, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	quit <- true

	log.Println("Shutting down")
	os.Exit(0)
}

func k8sClient() *kubernetes.Clientset {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientset
}

func main() {
	log.Println("Starting server")

	gitlabUrl := os.Getenv("GITLAB_API_URL")
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	gitlabApi, err := gitlab.NewClient(gitlabToken, gitlab.WithBaseURL(gitlabUrl))
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	clientSet := k8sClient()
	config := &AppConfig{
		repoPid:              os.Getenv("GITLAB_REPO_ID"),
		repoToken:            os.Getenv("GITLAB_TOKEN"),
		repoGitUrl:           os.Getenv("GITLAB_GIT_URL"),
		repoApiUrl:           os.Getenv("GITLAB_API_URL"),
		repoStagingBranch:    os.Getenv("GITLAB_BRANCH"),
		repoProductionBranch: os.Getenv("GITLAB_BRANCH"),

		registryPid:        os.Getenv("GITLAB_REGISTRY_ID"),
		registryMr:         os.Getenv("GITLAB_REGISTRY_MR"),
		registryStaging:    os.Getenv("GITLAB_REGISTRY_STAGING"),
		registryProduction: os.Getenv("GITLAB_REGISTRY_PRODUCTION"),
	}

	app := NewApp(gitlabApi, clientSet, config)
	app.Run()
}
