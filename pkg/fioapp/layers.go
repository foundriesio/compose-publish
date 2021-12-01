package fioapp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/compose-spec/compose-go/types"
	"github.com/distribution/distribution/v3/reference"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/foundriesio/compose-publish/internal"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"sort"
)

type (
	ArchManifestServices map[string]map[distribution.ManifestService]digest.Digest
)

func GetManifestService(ctx context.Context, regClient internal.RegistryClient, repoRef reference.Named) (distribution.ManifestService, error) {
	repo, err := regClient.GetRepository(ctx, repoRef)
	if err != nil {
		return nil, err
	}
	return repo.Manifests(ctx, nil)
}

func GetBlobService(ctx context.Context, regClient internal.RegistryClient, repoRef reference.Named) (distribution.BlobStore, error) {
	repo, err := regClient.GetRepository(ctx, repoRef)
	if err != nil {
		return nil, err
	}
	return repo.Blobs(ctx), nil
}

func GetManifest(ctx context.Context, manifestSvc distribution.ManifestService, digest digest.Digest) (*schema2.DeserializedManifest, error) {
	manifest, err := manifestSvc.Get(ctx, digest)
	if err != nil {
		return nil, err
	}
	if _, ok := manifest.(*schema2.DeserializedManifest); !ok {
		return nil, fmt.Errorf("invalid manifest type, expected schema2.DeserializedManifest, got: %T", manifest)
	}
	return manifest.(*schema2.DeserializedManifest), nil
}

func GetAppLayers(ctx context.Context, services map[string]types.ServiceConfig) (map[string][]distribution.Descriptor, error) {
	svcImages := make(map[string]string)
	for svc, svcCfg := range services {
		svcImages[svc] = svcCfg.Image
	}
	return GetAppLayersFromMap(ctx, svcImages)
}

func GetLayers(ctx context.Context, services map[string]interface{}) (map[string][]distribution.Descriptor, error) {
	svcImages := make(map[string]string)
	for svc, cfg := range services {
		svcCfg := cfg.(map[string]interface{})
		svcImages[svc] = svcCfg["image"].(string)
	}
	return GetAppLayersFromMap(ctx, svcImages)
}

func GetAppLayersFromMap(ctx context.Context, svcImages map[string]string) (map[string][]distribution.Descriptor, error) {
	regClient := internal.NewRegistryClient()

	// Get manifests per architecture and per image, for one architecture there should be one manifest for each service image
	// One image might have two or more manifests per the same architecture, it's odd, but is true, see nginx's manifest list
	// Thus, we have to created a map of maps, arch -> image/manifest-service -> manifest-digest, to avoid inclusion
	// more then manifest per image and per architecture
	archToManifestList := make(ArchManifestServices)
	for _, image := range svcImages {
		imageRef, err := reference.ParseNamed(image)
		if err != nil {
			return nil, err
		}

		imageManifestSvc, err := GetManifestService(ctx, regClient, imageRef)
		if err != nil {
			return nil, err
		}

		indexManifest, err := imageManifestSvc.Get(ctx, imageRef.(reference.Canonical).Digest())
		if err != nil {
			return nil, err
		}

		populateFromList := func(manifestList *manifestlist.DeserializedManifestList) {
			for _, manifest := range manifestList.Manifests {
				if _, ok := archToManifestList[manifest.Platform.Architecture]; !ok {
					archToManifestList[manifest.Platform.Architecture] = make(map[distribution.ManifestService]digest.Digest)
				}
				archToManifestList[manifest.Platform.Architecture][imageManifestSvc] = manifest.Digest
			}
		}

		populateFromManifest := func(manifest *schema2.DeserializedManifest) error {
			imageBlobSvc, err := GetBlobService(ctx, regClient, imageRef)
			if err != nil {
				return err
			}
			b, err := imageBlobSvc.Get(ctx, manifest.Config.Digest)
			if err != nil {
				return err
			}
			config := make(map[string]interface{})
			err = json.Unmarshal(b, &config)
			if err != nil {
				return err
			}
			arch := config["architecture"].(string)
			if _, ok := archToManifestList[arch]; !ok {
				archToManifestList[arch] = make(map[distribution.ManifestService]digest.Digest)
			}
			archToManifestList[arch][imageManifestSvc] = imageRef.(reference.Canonical).Digest()
			return nil
		}

		switch im := indexManifest.(type) {
		case *manifestlist.DeserializedManifestList:
			populateFromList(im)
		case *schema2.DeserializedManifest:
			err = populateFromManifest(im)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unexpected type of image manifest; image: %s, type: %T", imageRef.(reference.Canonical).String(), indexManifest)
		}
	}

	appLayers := make(map[string]map[string]distribution.Descriptor)
	expectedManNumber := len(svcImages)
	for arch, manifests := range archToManifestList {
		// Shortlist architectures, we need to include only architectures for which there is one manifest per each service image
		if len(manifests) != expectedManNumber {
			fmt.Printf("  |-> exclude  %s architecture, some of the app images (%d images) don't have manifest for it\n", arch, expectedManNumber-len(manifests))
			delete(archToManifestList, arch)
			continue
		}

		fmt.Printf("  |-> getting app layers for architecture: %s\n", arch)

		// we use map instead of slice/array of Descriptor in order to avoid layer duplication since
		// different images can consists of the same layers (layer intersection across images)
		appLayers[arch] = make(map[string]distribution.Descriptor)
		for manSvc, d := range manifests {
			manifest, err := GetManifest(ctx, manSvc, d)
			if err != nil {
				return nil, err
			}
			for _, layer := range manifest.Layers {
				appLayers[arch][layer.Digest.Encoded()] = layer
			}
		}
	}

	// now we need to sort layers in order to get consistent hash if app layers don't change
	sortedAppLayers := make(map[string][]distribution.Descriptor)

	for arch, layers := range appLayers {
		sortedLayerHashes := make([]string, len(layers))
		var indx int
		for layerHash := range layers {
			sortedLayerHashes[indx] = layerHash
			indx++
		}
		sort.Strings(sortedLayerHashes)

		sortedAppLayers[arch] = make([]distribution.Descriptor, len(layers))
		for ii, hash := range sortedLayerHashes {
			sortedAppLayers[arch][ii] = layers[hash]
		}
	}

	return sortedAppLayers, nil
}

func ComposeAppLayersManifest(arch string, layers []distribution.Descriptor) (distribution.Manifest, *distribution.Descriptor, error) {
	platform := v1.Platform{
		Architecture: arch,
		OS:           "LMP",
	}

	manifestDef := struct {
		manifest.Versioned
		Platform    *v1.Platform              `json:"platform"`
		Layers      []distribution.Descriptor `json:"layers"`
		Annotations map[string]string         `json:"annotations"`
	}{
		Versioned: manifest.Versioned{
			SchemaVersion: 2,
			MediaType:     v1.MediaTypeImageIndex,
		},
		Platform: &platform,
		Layers:   layers,
		Annotations: map[string]string{
			"compose-app-layers": "v1",
		},
	}
	manifestJson, err := json.Marshal(manifestDef)
	if err != nil {
		return nil, nil, err
	}
	man, desc, err := distribution.UnmarshalManifest(v1.MediaTypeImageIndex, manifestJson)
	if err != nil {
		return nil, nil, err
	}
	desc.Platform = &platform
	return man, &desc, nil
}

func PostAppLayersManifests(ctx context.Context, appRef string, layers map[string][]distribution.Descriptor) ([]distribution.Descriptor, error) {
	// sort layer lists by arch

	manifestDescArchs := make([]string, len(layers))
	var ii int
	for arch := range layers {
		manifestDescArchs[ii] = arch
		ii++
	}

	sort.Strings(manifestDescArchs)

	regClient := internal.NewRegistryClient()

	ref, err := reference.ParseNamed(appRef)
	if err != nil {
		return nil, err
	}

	manSvc, err := GetManifestService(ctx, regClient, ref)
	if err != nil {
		return nil, err
	}

	ii = 0
	manDescrs := make([]distribution.Descriptor, len(layers))
	for _, arch := range manifestDescArchs {
		manifest, desc, err := ComposeAppLayersManifest(arch, layers[arch])
		if err != nil {
			return nil, err
		}
		fmt.Printf("  |-> posting a layer manifest for architecture: %s...", arch)
		digest, err := manSvc.Put(ctx, manifest)
		if err != nil {
			return nil, err
		}
		if digest.Encoded() != (*desc).Digest.Encoded() {
			return nil, fmt.Errorf("digest of the posted manifest doesn't match to the composed manifest digest")
		}
		fmt.Printf("OK |-> digest: %s\n", digest)
		manDescrs[ii] = *desc
		ii++
	}
	return manDescrs, nil
}
