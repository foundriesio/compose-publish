package internal

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	distribution "github.com/distribution/distribution/v3"

	"github.com/distribution/reference"
	"github.com/docker/cli/cli/config"
	registrytypes "github.com/docker/docker/api/types/registry"
	"github.com/foundriesio/compose-publish/internal/client"
	"github.com/foundriesio/compose-publish/internal/client/auth"
	"github.com/foundriesio/compose-publish/internal/client/auth/challenge"
	"github.com/foundriesio/compose-publish/internal/client/transport"
	mobyregistry "github.com/moby/moby/registry"
	"github.com/pkg/errors"
)

// AuthConfigResolver returns Auth Configuration for an index
type AuthConfigResolver func(ctx context.Context, index *registrytypes.IndexInfo) registrytypes.AuthConfig

type RegistryClient struct {
	authConfigResolver AuthConfigResolver
	insecureRegistry   bool
	userAgent          string
}

func ResolveAuthConfig(ctx context.Context, index *registrytypes.IndexInfo) registrytypes.AuthConfig {
	cfg := config.LoadDefaultConfigFile(os.Stderr)
	a, _ := cfg.GetAuthConfig(index.Name)
	return registrytypes.AuthConfig(a)
}

func NewRegistryClient() RegistryClient {
	resolver := func(ctx context.Context, index *registrytypes.IndexInfo) registrytypes.AuthConfig {
		return ResolveAuthConfig(ctx, index)
	}

	return RegistryClient{
		authConfigResolver: resolver,
		insecureRegistry:   false,
		userAgent:          "Compose-Ref",
	}
}

func (c *RegistryClient) GetRepository(ctx context.Context, ref reference.Named) (distribution.Repository, error) {
	repoEndpoint, err := newDefaultRepositoryEndpoint(ref, c.insecureRegistry)
	if err != nil {
		return nil, err
	}

	return c.getRepositoryForReference(ctx, ref, repoEndpoint)
}

func (c *RegistryClient) getRepositoryForReference(ctx context.Context, ref reference.Named, repoEndpoint repositoryEndpoint) (distribution.Repository, error) {
	httpTransport, err := c.getHTTPTransportForRepoEndpoint(ctx, repoEndpoint)
	if err != nil {
		return nil, err
	}
	repoName, err := reference.WithName(repoEndpoint.Name())
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse repo name from %s", ref)
	}
	return client.NewRepository(repoName, repoEndpoint.BaseURL(), httpTransport)
}

func (c *RegistryClient) getHTTPTransportForRepoEndpoint(ctx context.Context, repoEndpoint repositoryEndpoint) (http.RoundTripper, error) {
	httpTransport, err := getHTTPTransport(
		c.authConfigResolver(ctx, repoEndpoint.info.Index),
		repoEndpoint.endpoint,
		repoEndpoint.Name(),
		c.userAgent)
	return httpTransport, errors.Wrap(err, "failed to configure transport")
}

// PingV2Registry attempts to ping a v2 registry and on success return a
// challenge manager for the supported authentication types.
// If a response is received but cannot be interpreted, a PingResponseError will be returned.
func PingV2Registry(endpoint *url.URL, transport http.RoundTripper) (challenge.Manager, error) {
	pingClient := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
	endpointStr := strings.TrimRight(endpoint.String(), "/") + "/v2/"
	req, err := http.NewRequest(http.MethodGet, endpointStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := pingClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	challengeManager := challenge.NewSimpleManager()
	if err := challengeManager.AddResponse(resp); err != nil {
		return nil, err
	}

	return challengeManager, nil
}

// getHTTPTransport builds a transport for use in communicating with a registry
func getHTTPTransport(authConfig registrytypes.AuthConfig, endpoint mobyregistry.APIEndpoint, repoName string, userAgent string) (http.RoundTripper, error) {
	// get the http transport, this will be used in a client to upload manifest
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig:     endpoint.TLSConfig,
		DisableKeepAlives:   true,
	}

	modifiers := []transport.RequestModifier{}
	if userAgent != "" {
		modifiers = append(modifiers, transport.NewHeaderRequestModifier(http.Header{
			"User-Agent": []string{userAgent},
		}))
	}
	authTransport := transport.NewTransport(base, modifiers...)
	challengeManager, err := PingV2Registry(endpoint.URL, authTransport)
	if err != nil {
		return nil, errors.Wrap(err, "error pinging v2 registry")
	}
	if authConfig.RegistryToken != "" {
		passThruTokenHandler := &existingTokenHandler{token: authConfig.RegistryToken}
		modifiers = append(modifiers, auth.NewAuthorizer(challengeManager, passThruTokenHandler))
	} else {
		creds := mobyregistry.NewStaticCredentialStore(&authConfig)
		tokenHandler := auth.NewTokenHandler(authTransport, creds, repoName, "push", "pull")
		basicHandler := auth.NewBasicHandler(creds)
		modifiers = append(modifiers, auth.NewAuthorizer(challengeManager, tokenHandler, basicHandler))
	}
	return transport.NewTransport(base, modifiers...), nil
}

type existingTokenHandler struct {
	token string
}

func (th *existingTokenHandler) AuthorizeRequest(req *http.Request, params map[string]string) error {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", th.token))
	return nil
}

func (th *existingTokenHandler) Scheme() string {
	return "bearer"
}

type repositoryEndpoint struct {
	info     *mobyregistry.RepositoryInfo
	endpoint mobyregistry.APIEndpoint
}

// Name returns the repository name
func (r repositoryEndpoint) Name() string {
	repoName := r.info.Name.Name()
	// If endpoint does not support CanonicalName, use the RemoteName instead
	if r.endpoint.TrimHostname {
		repoName = reference.Path(r.info.Name)
	}
	return repoName
}

// BaseURL returns the endpoint url
func (r repositoryEndpoint) BaseURL() string {
	return r.endpoint.URL.String()
}

func newDefaultRepositoryEndpoint(ref reference.Named, insecure bool) (repositoryEndpoint, error) {
	repoInfo, err := mobyregistry.ParseRepositoryInfo(ref)
	if err != nil {
		return repositoryEndpoint{}, err
	}
	endpoint, err := getDefaultEndpointFromRepoInfo(repoInfo)
	if err != nil {
		return repositoryEndpoint{}, err
	}
	if insecure {
		endpoint.TLSConfig.InsecureSkipVerify = true
	}
	return repositoryEndpoint{info: repoInfo, endpoint: endpoint}, nil
}

func getDefaultEndpointFromRepoInfo(repoInfo *mobyregistry.RepositoryInfo) (mobyregistry.APIEndpoint, error) {
	var err error

	options := mobyregistry.ServiceOptions{}
	registryService, err := mobyregistry.NewService(options)
	if err != nil {
		return mobyregistry.APIEndpoint{}, err
	}
	endpoints, err := registryService.LookupPushEndpoints(reference.Domain(repoInfo.Name))
	if err != nil {
		return mobyregistry.APIEndpoint{}, err
	}
	// Default to the highest priority endpoint to return
	endpoint := endpoints[0]
	if !repoInfo.Index.Secure {
		for _, ep := range endpoints {
			if ep.URL.Scheme == "http" {
				endpoint = ep
			}
		}
	}
	return endpoint, nil
}
