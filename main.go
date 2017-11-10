package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/mobingilabs/mobingi-sdk-go/mobingi/registry/pullr"
	"github.com/mobingilabs/mobingi-sdk-go/pkg/cmdline"
	"github.com/mobingilabs/mobingi-sdk-go/pkg/debug"
	"github.com/spf13/cobra"
)

const (
	MaxFailedRead = 10
)

var (
	// main parent (root) command
	rootCmd = &cobra.Command{
		Use:   "icariumd",
		Short: "pullr docker auto-builder",
		Long:  `Docker auto-builder service for pullr.`,
		Run:   run,
	}
)

type pullrMessage struct {
	Action string            `json:"action"`
	Data   map[string]string `json:"data"`
}

func run(cmd *cobra.Command, args []string) {
	var qc pullr.QueueClient
	var wg sync.WaitGroup

	running := true
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		_ = <-sigs
		running = false
		debug.Info("Exiting...")
	}()

	_, err := session.NewSession(&aws.Config{})
	if err != nil {
		debug.ErrorExit(err, 1)
	}

	failedReadAttempts := 0
	for running {
		_, messages, _, err := qc.Read(nil)
		if err != nil {
			debug.Error(err)
			failedReadAttempts++
			if failedReadAttempts >= MaxFailedRead {
				debug.Error("Attempt to fetch messages from pullr queue failed too many times...")
				break
			}
			time.Sleep(1 * time.Second)
			continue
		}

		failedReadAttempts = 0

		for i := 0; i < len(messages); i++ {
			var awsMessage map[string]string
			if err := json.Unmarshal([]byte(messages[i]), &awsMessage); err != nil {
				debug.Error(err)
				continue
			}

			var msg pullrMessage
			if err := json.Unmarshal([]byte(awsMessage["Message"]), &msg); err != nil {
				debug.Error(err)
				continue
			}

			switch msg.Action {
			case "build":
				wg.Add(1)
				go func(msg pullrMessage) {
					defer wg.Done()
					if err := build(msg.Data["provider"], msg.Data["repository"]); err != nil {
						debug.Error(err)
						return
					}
				}(msg)
			default:
				debug.Info("Unkown action: ", msg.Action)
			}
		}
	}

	debug.Info("Waiting for the tasks to complete before exit...")
	wg.Wait()
}

func main() {
	log.SetFlags(0)
	pfx := "[" + cmdline.Args0() + "]: "
	log.SetPrefix(pfx)

	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}

func build(provider, repositoryFullname string) error {
	debug.Info("Building", provider, repositoryFullname)
	if provider != "github" {
		return fmt.Errorf("Unsupported provider %s", provider)
	}

	githubToken, err := getGithubToken(repositoryFullname)
	if err != nil {
		return err
	}

	repositoryParts := strings.Split(repositoryFullname, "/")
	username := repositoryParts[0]
	repository := repositoryParts[1]

	userPath := filepath.Join(os.TempDir(), "icarium", username)
	clonePath := filepath.Join(userPath, repository)
	//os.MkdirAll(userPath, )

	if err := cloneGithubRepository(githubToken, clonePath, repositoryFullname); err != nil {
		return err
	}

	buildPath := filepath.Join(clonePath, repository)
	if err := buildDockerImage(buildPath); err != nil {
		return err
	}

	if err := pushDockerImage("[someurlhere]"); err != nil {
		return err
	}

	return nil
}

func cloneGithubRepository(githubToken, path, repository string) error {
	debug.Info("Clonning repository", repository, "into", path)
	// FIXME: This will save token to .git/config, possible security risk
	cloneUrl := fmt.Sprintf("https://%s@github.com/%s", githubToken, repository)
	_ = exec.Command("git", "clone", cloneUrl)
	return nil
}

func getGithubToken(repository string) (string, error) {
	debug.Info("Getting github token for repository ", repository)
	return "", nil
}

func buildDockerImage(path string) error {
	debug.Info("Building docker image in", path)
	return nil
}

func pushDockerImage(url string) error {
	debug.Info("Pushing docker image to", url)
	return nil
}
