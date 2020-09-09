package main

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/compose-spec/compose-go/loader"
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
		},
		Action: func(c *commandLine.Context) error {
			target := c.Args().Get(0)
			if len(target) == 0 {
				return errors.New("Missing required argument: TARGET:[TAG]")
			}
			return doPublish(file, target)
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

func doPublish(file, target string) error {
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

	fmt.Println("= Pinning service images...")
	svcs, ok := config["services"]
	if !ok {
		return errors.New("Unable to find 'services' section of compose file")
	}
	if err := internal.PinServiceImages(cli, ctx, svcs.(map[string]interface{})); err != nil {
		return err
	}

	fmt.Println("= Publishing app...")
	return internal.CreateApp(ctx, config, target)
}
