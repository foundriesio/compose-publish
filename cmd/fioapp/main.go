package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/compose-spec/compose-go/cli"
	"github.com/compose-spec/compose-go/types"
	"github.com/foundriesio/compose-publish/pkg/fioapp"
	"log"
	"path/filepath"
	"strings"
)

func main() {
	var composeFile string
	var appRef string
	var archListStr string

	flag.StringVar(&composeFile, "compose-file", "docker-compose.yml", "A path to a compose file")
	flag.StringVar(&appRef, "app-ref", "", "A reference to App's Registry Repo")
	flag.StringVar(&archListStr, "arch-list", "", "An architecture list")
	flag.Parse()

	if len(appRef) == 0 {
		log.Fatalf("mandatory parameter `app-ref` is not defined")
	}

	appProj, err := getAppProject(composeFile)
	if err != nil {
		log.Fatalf("failed to parse App: %s", err.Error())
	}

	svcs, err := appProj.Services.MarshalYAML()
	if err != nil {
		log.Fatalf("failed to marshal App services into map: %s", err.Error())
	}
	appServices := svcs.(map[string]types.ServiceConfig)

	ctx := context.Background()

	var archList []string
	if len(archListStr) > 0 {
		archList = strings.Split(archListStr, ",")
	}
	appLayers, err := fioapp.GetAppLayers(ctx, appServices, archList)
	if err != nil {
		log.Fatalf("failed to get App layers: %s", err.Error())
	}

	fmt.Println("App layers:")
	for arch, layers := range appLayers {
		fmt.Printf("\tarch: %s\n", arch)

		for _, l := range layers {
			fmt.Printf("\t\tdigest: %s\n", l.Digest)
			fmt.Printf("\t\tsize: %d\n", l.Size)
		}
	}

	layerManifests, err := fioapp.PostAppLayersManifests(ctx, appRef, appLayers)
	if err != nil {
		log.Fatalf("failed to generate or post App layers manifest: %s", err.Error())
	}

	b, err := json.MarshalIndent(&layerManifests, "", "\t")
	if err != nil {
		log.Fatalf("failed to marshal a layer imdex manifest: %s", err.Error())
	}
	fmt.Printf("Manifests json\n%s\n", string(b))
}

func getAppProject(composeFile string) (*types.Project, error) {
	wd := filepath.Dir(composeFile)
	opts := cli.ProjectOptions{
		Name:        "ComposeApp",
		WorkingDir:  wd,
		ConfigPaths: []string{composeFile},
	}
	prj, err := cli.ProjectFromOptions(&opts)
	if err != nil {
		return nil, err
	}
	return prj, nil
}
