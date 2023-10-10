package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cta.epfl.ch/mr-feature-controller/git"

	"github.com/Masterminds/semver/v3"
	"github.com/xanzy/go-gitlab"
)

type MrDeployStatus int64

const (
	NotDeployed MrDeployStatus = iota
	UpToDate
	UpdateAvailable
	Pending
	Desynchronized
)

const IMAGE_REGISTRY = "gitlab.cta-observatory.org:5555/bastien.wermeille/ctao-esap-sdc-portal/esap-mr"
const STAGING_BRANCH = "main"
const PRODUCTION_BRANCH = "main"

// TODO: Create new config struct -> including pid, target_branch and so on
type App struct {
	gitlab *gitlab.Client
	pid    string

	mrCommits     map[string]bool
	prodCommits   map[string]bool
	stagingCommit map[string]bool
}

func NewApp(gitlabApi *gitlab.Client, pid string) *App {
	return &App{
		gitlab: gitlabApi,
		pid:    pid,

		mrCommits:     map[string]bool{},
		prodCommits:   map[string]bool{},
		stagingCommit: map[string]bool{},
	}
}

func (app *App) build(contextDir string, dockerfile string, imageName string, imageTag string) error {
	// TODO: launch kaniko with the right context, dockerfile and registryTag
	cmd := exec.Command("/kaniko/executor",
		"--context", contextDir,
		"--dockerfile", filepath.Join(contextDir, dockerfile),
		"--destination", imageName+":"+imageTag)

	// TODO: Launch a few build instances in parallele
	return cmd.Run()
}

func (app *App) prepareEnv(branch string, commit string) (string, error) {
	folder, err := os.MkdirTemp("", "tmp")
	if err != nil {
		return "", err
	}

	// clone env at provided commit
	token := os.Getenv("GITLAB_TOKEN")
	repository := os.Getenv("GITLAB_URL")

	gitUrl := strings.Replace(repository, "https://", "https://oauth2:"+token+"@", 1)

	g, err := git.NewGit(folder, gitUrl, branch)
	if err != nil {
		return "", fmt.Errorf("unable to clone repo: %w", err)
	}
	err = g.Checkout(commit)
	if err != nil {
		return "", fmt.Errorf("unable to checkout commit: %w", err)
	}
	return folder, nil
}

func (app *App) loopMr() {
	// TODO: Extract in option struct
	targetBranch := os.Getenv("TARGET_BRANCH")
	projectId := os.Getenv("GITLAB_PROJECT_ID")

	// TODO: Loop production -> tags
	// TODO: Loop staging -> most recent commit on a given branch
	// TODO: Loop MR -> most recent commit on a given MR

	openedState := "opened"
	openMergeRequests, _, err := app.gitlab.MergeRequests.ListProjectMergeRequests(projectId, &gitlab.ListProjectMergeRequestsOptions{
		TargetBranch: &targetBranch,
		State:        &openedState,
	})

	if err != nil {
		log.Printf("Unable to load MR")
		return
	}

	for _, mergeRequest := range openMergeRequests {
		commits, _, err := app.gitlab.MergeRequests.GetMergeRequestCommits(app.pid, mergeRequest.ID, &gitlab.GetMergeRequestCommitsOptions{PerPage: 1})
		// Latest commit
		if err != nil || len(commits) != 1 {
			log.Printf("No commit for MR %d", mergeRequest.ID)
			continue
		}
		latestCommit := commits[0]

		if _, ok := app.stagingCommit[latestCommit.ID]; !ok {
			// Prepare environment
			envFolder, err := app.prepareEnv(mergeRequest.SourceBranch, latestCommit.ID)
			if err != nil {
				log.Printf("Error while cloning MR environement: %s", err)
				continue
			}

			// Build image
			err = app.build(envFolder, "esap/Dockerfile", IMAGE_REGISTRY, latestCommit.ID)
			app.prodCommits[latestCommit.ID] = true

			if err != nil {
				log.Printf("Error while building MR image %s: %s", mergeRequest.ID, err)
			}
		}
	}
}

func (app *App) loopStaging() {
	// Load latest commit
	branche, _, err := app.gitlab.Branches.GetBranch(app.pid, STAGING_BRANCH)
	if err != nil {
		log.Printf("Unable to load branch: %s", STAGING_BRANCH)
		return
	}

	if _, ok := app.stagingCommit[branche.Commit.ID]; !ok {
		// Prepare environment
		envFolder, err := app.prepareEnv(STAGING_BRANCH, branche.Commit.ID)
		if err != nil {
			log.Printf("Error while cloning staging environement: %s", err)
			return
		}

		// Build image
		versionId := strconv.Itoa(int(branche.Commit.CommittedDate.Unix()))
		err = app.build(envFolder, "esap/Dockerfile", IMAGE_REGISTRY, versionId)
		app.prodCommits[versionId] = true

		if err != nil {
			log.Printf("Error while building staging image '%s': %s", versionId, err)
		}
	}
}

func (app *App) loopProduction() {
	// Load latest tag
	tags, _, err := app.gitlab.Tags.ListTags(app.pid, &gitlab.ListTagsOptions{})
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

	if _, ok := app.prodCommits[latestTag]; !ok {
		// Prepare environment
		envFolder, err := app.prepareEnv(PRODUCTION_BRANCH, latestTagCommit)
		if err != nil {
			log.Printf("Error while cloning production environement: %s", err)
			return
		}

		// Build image
		err = app.build(envFolder, "esap/Dockerfile", IMAGE_REGISTRY, latestTag)
		app.prodCommits[latestTag] = true

		if err != nil {
			log.Printf("Error while building prod image '%s': %s", latestTagCommit, err)
		}
	}
}

func (app *App) loop() {
	// TODO: implement
	// 0. Local store of the latest built images per MR
	// 1. Load images from registry
	// 2. Load latest commit tag from repository
	// 3. Launch a build for the image
	// 4. Clean registry built images for MR closed
	// 5. Configure build for staging and prod

	app.loopProduction()
	app.loopStaging()
	app.loopMr()
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

func main() {
	log.Println("Starting server")

	gitlabUrl := os.Getenv("GITLAB_URL")
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	gitlabApi, err := gitlab.NewClient(gitlabToken, gitlab.WithBaseURL(gitlabUrl))
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	app := NewApp(gitlabApi, os.Getenv("GITLAB_PROJECT_ID"))
	app.Run()
}
