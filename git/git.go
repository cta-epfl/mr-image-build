package git

import (
	"bytes"
	"fmt"
	"os/exec"
)

type Repository struct {
	Repository string
	Folder     string
}

func NewGit(folder string, repo string, branch string) (*Repository, error) {
	g := &Repository{
		Repository: repo,
		Folder:     folder,
	}

	cmd := exec.Command("git", "config", "--global", "user.name", "mrcontroller[bot]")
	cmd.Run()
	cmd = exec.Command("git", "config", "--global", "user.email", "mrcontroller[bot]@epfl.ch")
	cmd.Run()

	if branch != "" {
		cmd = exec.Command("git", "clone", "--branch", branch, repo, folder)
	} else {
		cmd = exec.Command("git", "clone", repo, folder)
	}
	cmd.Dir = g.Folder
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		fmt.Println("out:", outb.String(), "err:", errb.String())
	}

	return g, err
}

func (g *Repository) Checkout(commit string) error {
	cmd := exec.Command("git", "checkout", commit)
	cmd.Dir = g.Folder
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		fmt.Println("out:", outb.String(), "err:", errb.String())
	}
	return err
}

func (g *Repository) AddAll() error {
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = g.Folder
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		fmt.Println("out:", outb.String(), "err:", errb.String())
	}
	return err
}

func (g *Repository) Commit(message string) error {
	cmd := exec.Command("git", "commit", "-m", "\""+message+"\"")
	cmd.Dir = g.Folder
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		fmt.Println("out:", outb.String(), "err:", errb.String())
	}
	return err
}

func (g *Repository) Push() error {
	cmd := exec.Command("git", "push", "-u", "origin", "main")
	cmd.Dir = g.Folder
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		fmt.Println("out:", outb.String(), "err:", errb.String())
	}
	return err
}

func (g *Repository) Pull() error {
	cmd := exec.Command("git", "pull")
	cmd.Dir = g.Folder
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	fmt.Println("out:", outb.String(), "err:", errb.String())
	return err
}
