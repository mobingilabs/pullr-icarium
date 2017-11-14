package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/mobingilabs/mobingi-sdk-go/mobingi/registry/pullr"
	"github.com/mobingilabs/mobingi-sdk-go/pkg/cmdline"
	"github.com/mobingilabs/mobingi-sdk-go/pkg/debug"
	"github.com/spf13/cobra"
)

const (
	MaxFailedRead = 10
)

var (
	dynamo *dynamodb.DynamoDB
	// main parent (root) command
	rootCmd = &cobra.Command{
		Use:   "icariumd",
		Short: "pullr docker auto-builder",
		Long:  `Docker auto-builder service for pullr.`,
		Run:   run,
	}
)

type pullrMessage struct {
	Action string `json:"action"`
	Data   struct {
		Provider   string `json:"provider"`
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		Commit     string `json:"commit"`
	} `json:"data"`
}

type pullrRepo struct {
	Repository string
	Username   string
	Tags       []pullrRepoTag
}

func (r *pullrRepo) findMatchingTag(ref string) (*pullrRepoTag, error) {
	for _, tag := range r.Tags {
		matches, err := tag.matchesRef(ref)
		if err != nil {
			return nil, err
		}

		if matches {
			return &tag, nil
		}
	}

	return nil, nil
}

type pullrRepoTag struct {
	Type               string
	Name               string
	DockerTag          string
	DockerfileLocation string
}

func (t *pullrRepoTag) matchesRef(ref string) (bool, error) {
	parts := strings.Split(ref, "/")
	headsOrTags := parts[1]
	lastPart := parts[len(parts)-1]

	if t.Type == "tag" {
		if headsOrTags != "tags" {
			return false, nil
		}

		// Check for regexp filtering
		if strings.HasPrefix(t.Name, "/") && strings.HasSuffix(t.Name, "/") {
			r, err := regexp.Compile(t.Name[1 : len(t.Name)-1])
			if err != nil {
				return false, err
			}

			return r.MatchString(parts[len(parts)-1]), nil
		}

		return lastPart == t.Name, nil
	}

	if headsOrTags != "heads" {
		return false, nil
	}

	return lastPart == t.Name, nil
}

func run(cmd *cobra.Command, args []string) {
	var qc pullr.QueueClient
	var wg sync.WaitGroup

	rand.Seed(time.Now().UnixNano())

	running := true
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		_ = <-sigs
		running = false
		debug.Info("Exiting...")
	}()

	awssess, err := session.NewSession(&aws.Config{})
	if err != nil {
		debug.ErrorExit(err, 1)
	}
	dynamo = dynamodb.New(awssess)

	failedReadAttempts := 0
	debug.Info("Icarium start waiting for the messages...")
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
					if err := build(msg.Data.Provider, msg.Data.Repository, msg.Data.Ref, msg.Data.Commit); err != nil {
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

func build(provider, repositoryFullname, ref, commit string) error {
	debug.Info("Building", provider, repositoryFullname)
	if provider != "github" {
		return fmt.Errorf("Unsupported provider %s", provider)
	}

	debug.Info("Finding pullr repository entry", repositoryFullname)
	repo, err := getPullrRepository("github", repositoryFullname)
	if err != nil {
		return err
	}

	debug.Info("Finding matching docker tag entry for the push ref", ref)
	tag, err := repo.findMatchingTag(ref)
	if err != nil {
		return err
	}

	if tag == nil {
		debug.Info("No matching tag entry found for ref", ref)
		return nil
	}

	debug.Info("Getting github token for user", repo.Username)
	githubToken, err := getGithubToken(repo.Username)
	if err != nil {
		return err
	}

	repositoryParts := strings.Split(repositoryFullname, "/")
	username := repositoryParts[0]
	repository := repositoryParts[1]

	userPath := filepath.Join(os.TempDir(), "icarium", username)
	clonePath := filepath.Join(userPath, repository, strconv.FormatInt(rand.Int63(), 10))
	os.MkdirAll(userPath, 0700)
	defer os.RemoveAll(clonePath)

	cloneErr := cloneGithubRepository(githubToken, clonePath, repositoryFullname, commit)
	if cloneErr != nil {
		return cloneErr
	}

	tagName := tag.DockerTag
	if tag.Type == "tag" && tag.DockerTag == "" {
		refParts := strings.Split(ref, "/")
		tagName = refParts[len(refParts)-1]
	}

	debug.Info("Building docker image in", clonePath, "with tag", repository, ":", tagName)
	if err := buildDockerImage(clonePath, ref, repository, tagName); err != nil {
		return err
	}

	if err := pushDockerImage("[someurlhere]"); err != nil {
		return err
	}

	return nil
}

func cloneGithubRepository(githubToken, clonePath, repository, commit string) error {
	debug.Info("Clonning repository", repository, "into", clonePath, "with token:", githubToken)
	// FIXME: This will save token to .git/config, possible security risk, altough we gonna remove the directory after build
	cloneUrl := fmt.Sprintf("https://%s@github.com/%s.git", githubToken, repository)
	debug.Info("Clone url:", cloneUrl)
	cloneCmd := exec.Command("git", "clone", cloneUrl, clonePath)
	cloneErr := cloneCmd.Run()
	if cloneErr != nil {
		return cloneErr
	}

	checkoutCmd := exec.Command("git", "checkout", commit)
	checkoutCmd.Dir = clonePath
	return checkoutCmd.Run()
}

func getPullrRepository(provider, repository string) (*pullrRepo, error) {
	repositoryPair := fmt.Sprintf("github:%s", repository)
	getInput := &dynamodb.GetItemInput{
		Key: map[string]*dynamodb.AttributeValue{
			"repository": {S: aws.String(repositoryPair)},
		},
		TableName: aws.String("PULLR_REPOS"),
	}

	result, err := dynamo.GetItem(getInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == dynamodb.ErrCodeResourceNotFoundException {
				return nil, nil
			}
		}
		return nil, err
	}

	tags := make([]pullrRepoTag, len(result.Item["tags"].L))
	for i, tag := range result.Item["tags"].L {
		_type := ""
		if tag.M["type"] != nil && tag.M["type"].S != nil {
			_type = *tag.M["type"].S
		}

		name := ""
		if tag.M["name"] != nil && tag.M["name"].S != nil {
			name = *tag.M["name"].S
		}

		dockerfileLocation := ""
		if tag.M["dockerfileLocation"] != nil && tag.M["dockerfileLocation"].S != nil {
			dockerfileLocation = *tag.M["dockerfileLocation"].S
		}

		dockerTagName := ""
		if tag.M["dockerTag"] != nil && tag.M["dockerTag"].S != nil {
			dockerTagName = *tag.M["dockerTag"].S
		}

		tags[i] = pullrRepoTag{
			Type:               _type,
			Name:               name,
			DockerTag:          dockerTagName,
			DockerfileLocation: dockerfileLocation,
		}
	}

	return &pullrRepo{
		Repository: *result.Item["repository"].S,
		Username:   *result.Item["username"].S,
		Tags:       tags,
	}, nil
}

func getGithubToken(username string) (string, error) {
	getInput := &dynamodb.GetItemInput{
		Key: map[string]*dynamodb.AttributeValue{
			"username": {S: aws.String(username)},
		},
		TableName: aws.String("MC_IDENTITY"),
	}

	result, err := dynamo.GetItem(getInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == dynamodb.ErrCodeResourceNotFoundException {
				return "", nil
			}
		}
		return "", err
	}

	githubTokenItem, ok := result.Item["github_token"]
	if !ok || githubTokenItem.S == nil {
		return "", nil
	}

	return *githubTokenItem.S, nil
}

func buildDockerImage(path, ref, imageName, tagName string) error {
	cmd := exec.Command("docker", "build", "-t", fmt.Sprintf("%s:%s", imageName, tagName), path)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func pushDockerImage(url string) error {
	debug.Info("Pushing docker image to", url)
	return nil
}
