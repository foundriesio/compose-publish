package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/compose-spec/compose-go/loader"
	compose "github.com/compose-spec/compose-go/types"
	"github.com/docker/docker/client"
	commandLine "github.com/urfave/cli/v2"

	"github.com/foundriesio/compose-publish/internal"
)

const banner = `
	   |\/|
	\__|__|__/

`

func main() {
	var file string
	var digestFile string
	var dryRun bool

	fmt.Print(banner)
	app := &commandLine.App{
		Name:  "compose-ref",
		Usage: "Reference Compose Specification implementation",
		Flags: []commandLine.Flag{
			&commandLine.StringFlag{
				Name:        "file",
				Aliases:     []string{"f"},
				Value:       "docker-compose.yml",
				Usage:       "Load Compose file `FILE`",
				Destination: &file,
			},
			&commandLine.StringFlag{
				Name:        "digest-file",
				Aliases:     []string{"d"},
				Required:    false,
				Usage:       "Save sha256 digest of bundle to a file",
				Destination: &digestFile,
			},
			&commandLine.BoolFlag{
				Name:        "dryrun",
				Required:    false,
				Usage:       "Show what would be done, but don't actually publish",
				Destination: &dryRun,
			},
		},
		Action: func(c *commandLine.Context) error {
			target := c.Args().Get(0)
			if len(target) == 0 {
				return errors.New("Missing required argument: TARGET:[TAG]")
			}
			return doPublish(file, target, digestFile, dryRun)
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func getClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil, err
	}
	cli.NegotiateAPIVersion(context.Background())
	return cli, nil
}

func loadProj(file string, config map[string]interface{}) (*compose.Project, error) {
	env := make(map[string]string)
	for _, val := range os.Environ() {
		parts := strings.Split(val, "=")
		env[parts[0]] = parts[1]
	}

	var files []compose.ConfigFile
	files = append(files, compose.ConfigFile{Filename: file, Config: config})
	return loader.Load(compose.ConfigDetails{
		WorkingDir:  ".",
		ConfigFiles: files,
		Environment: env,
	})
}

func doPublish(file, target, digestFile string, dryRun bool) error {
	b, err := ioutil.ReadFile(file)
	if err != nil {
		return err
	}
	config, err := loader.ParseYAML(b)
	if err != nil {
		return err
	}
	cli, err := getClient()
	if err != nil {
		return err
	}

	ctx := context.Background()

	proj, err := loadProj(file, config)
	if err != nil {
		return err
	}

	fmt.Println("= Pinning service images...")
	svcs, ok := config["services"]
	if !ok {
		return errors.New("Unable to find 'services' section of compose file")
	}
	if err := internal.PinServiceImages(cli, ctx, svcs.(map[string]interface{}), proj); err != nil {
		return err
	}

	fmt.Println("= Publishing app...")
	dgst, err := internal.CreateApp(ctx, config, target, dryRun)
	if err != nil {
		return err
	}
	if len(digestFile) > 0 {
		return ioutil.WriteFile(digestFile, []byte(dgst), 0o640)
	}
	return nil
}
