package testhelper

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	"github.com/docker/go-connections/nat"
	"github.com/pkg/errors"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dbUser                   = "mmuser"
	dbPassword               = "mostest"
	dbName                   = "mattermost_test"
	mattermostPort           = "8065/tcp"
	postgresAlias            = "postgres"
	postgresStartupTimeout   = 30 * time.Second
	mattermostStartupTimeout = 120 * time.Second

	defaultMattermostImage = "mattermost/mattermost-team-edition"
	defaultMattermostTag   = "10.5"
	envMattermostImage     = "MMETL_E2E_MATTERMOST_IMAGE"
	envMattermostVersion   = "MMETL_E2E_MATTERMOST_VERSION"
	dockerHubTagsURLFmt    = "https://hub.docker.com/v2/repositories/%s/%s/tags/"
)

var (
	resolvedMattermostImage string
	resolveOnce             sync.Once
)

var semverTagRegexp = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// dockerHubTagsResponse represents the response from Docker Hub's tag listing API.
type dockerHubTagsResponse struct {
	Next    string `json:"next"`
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
}

// findHighestStableVersion takes a list of Docker Hub tag names and returns
// the highest version that matches the strict X.Y.Z semver pattern.
// Tags like RCs, short tags (e.g. "10.5"), and branch names are excluded.
func findHighestStableVersion(tagNames []string) (string, error) {
	var versions []semver.Version
	for _, tag := range tagNames {
		if !semverTagRegexp.MatchString(tag) {
			continue
		}
		v, err := semver.Parse(tag)
		if err != nil {
			continue
		}
		versions = append(versions, v)
	}

	if len(versions) == 0 {
		return "", fmt.Errorf("no stable versions found")
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].LT(versions[j])
	})

	return versions[len(versions)-1].String(), nil
}

// fetchLatestStableTag queries Docker Hub for the given image and returns
// the highest stable X.Y.Z tag found.
func fetchLatestStableTag(image string) (string, error) {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid image name %q: expected namespace/repository", image)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf(dockerHubTagsURLFmt+"?page_size=100", parts[0], parts[1])

	const maxPages = 5
	var allTags []string
	for page := 0; url != "" && page < maxPages; page++ {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", errors.Wrap(err, "creating request")
		}

		resp, err := client.Do(req)
		if err != nil {
			return "", errors.Wrap(err, "fetching tags from Docker Hub")
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("docker hub returned status %d for %s", resp.StatusCode, url)
		}

		var tagsResp dockerHubTagsResponse
		if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
			resp.Body.Close()
			return "", errors.Wrap(err, "decoding Docker Hub response")
		}
		resp.Body.Close()

		for _, r := range tagsResp.Results {
			allTags = append(allTags, r.Name)
		}

		url = tagsResp.Next
	}

	return findHighestStableVersion(allTags)
}

// resolveMattermostImage determines the Mattermost container image to use.
// Resolution priority:
//  1. MMETL_E2E_MATTERMOST_IMAGE env var (full image:tag)
//  2. MMETL_E2E_MATTERMOST_VERSION env var (combined with default image name)
//  3. Auto-resolve latest stable tag from Docker Hub
//  4. Fall back to defaultMattermostImage:defaultMattermostTag
func resolveMattermostImage() string {
	resolveOnce.Do(func() {
		// 1. Full image override
		if img := os.Getenv(envMattermostImage); img != "" {
			resolvedMattermostImage = img
			log.Printf("mmetl e2e: using Mattermost image from %s: %s", envMattermostImage, resolvedMattermostImage)
			return
		}

		// 2. Version override
		if ver := os.Getenv(envMattermostVersion); ver != "" {
			resolvedMattermostImage = defaultMattermostImage + ":" + ver
			log.Printf("mmetl e2e: using Mattermost image from %s: %s", envMattermostVersion, resolvedMattermostImage)
			return
		}

		// 3. Auto-resolve from Docker Hub
		tag, err := fetchLatestStableTag(defaultMattermostImage)
		if err != nil {
			log.Printf("mmetl e2e: failed to resolve latest Mattermost tag: %v; falling back to %s:%s", err, defaultMattermostImage, defaultMattermostTag)
			resolvedMattermostImage = defaultMattermostImage + ":" + defaultMattermostTag
			return
		}

		resolvedMattermostImage = defaultMattermostImage + ":" + tag
		log.Printf("mmetl e2e: resolved Mattermost image: %s", resolvedMattermostImage)
	})

	return resolvedMattermostImage
}

// TearDownFunc is a function that cleans up test containers
type TearDownFunc func(ctx context.Context) error

// ContainerSetup holds the network and containers for the test environment
type ContainerSetup struct {
	Network           *testcontainers.DockerNetwork
	PostgresContainer testcontainers.Container
	TearDowns         []TearDownFunc
}

// CreateTestNetwork creates a Docker network for the test containers
func CreateTestNetwork(ctx context.Context) (*testcontainers.DockerNetwork, TearDownFunc, error) {
	net, err := network.New(ctx)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create docker network")
	}

	tearDown := func(ctx context.Context) error {
		return net.Remove(ctx)
	}

	return net, tearDown, nil
}

// CreatePostgresContainer creates a PostgreSQL container for testing and returns the connection string
func CreatePostgresContainer(ctx context.Context, networkName string) (testcontainers.Container, string, TearDownFunc, error) {
	postgresContainer, err := postgres.Run(ctx,
		"docker.io/library/postgres:15.2-alpine",
		postgres.WithDatabase(dbName),
		postgres.WithUsername(dbUser),
		postgres.WithPassword(dbPassword),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(postgresStartupTimeout),
		),
		network.WithNetworkName([]string{postgresAlias}, networkName),
	)
	if err != nil {
		return nil, "", nil, errors.Wrap(err, "failed to start postgres container")
	}

	tearDown := func(ctx context.Context) error {
		return postgresContainer.Terminate(ctx)
	}

	// Connection string for host access (for debugging)
	connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = postgresContainer.Terminate(ctx)
		return nil, "", nil, errors.Wrap(err, "cannot generate connection string for postgres")
	}

	return postgresContainer, connStr, tearDown, nil
}

// GetPostgresInternalConnStr returns the connection string for use inside the Docker network
func GetPostgresInternalConnStr() string {
	return fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable", dbUser, dbPassword, postgresAlias, dbName)
}

// CreateMattermostContainer creates a Mattermost server container connected to the given PostgreSQL database
func CreateMattermostContainer(ctx context.Context, networkName string) (testcontainers.Container, string, TearDownFunc, error) {
	// Use the internal Docker network address for PostgreSQL
	postgresConnStr := GetPostgresInternalConnStr()

	imagePlatform := ""
	if runtime.GOOS == "darwin" {
		imagePlatform = "linux/amd64"
	}

	req := testcontainers.ContainerRequest{
		Image:         resolveMattermostImage(),
		ImagePlatform: imagePlatform,
		ExposedPorts:  []string{mattermostPort},
		Networks:      []string{networkName},
		Env: map[string]string{
			"MM_SQLSETTINGS_DRIVERNAME": "postgres",
			"MM_SQLSETTINGS_DATASOURCE": postgresConnStr,
			// SiteURL is set to the container-internal address because the
			// host-mapped port is not known until after the container starts.
			// This only affects Mattermost's self-referential links (e.g. email
			// notifications), which are not exercised by these tests.
			"MM_SERVICESETTINGS_SITEURL":                  "http://localhost:8065",
			"MM_SERVICESETTINGS_LISTENADDRESS":            ":8065",
			"MM_PASSWORDSETTINGS_MINIMUMLENGTH":           "5",
			"MM_TEAMSETTINGS_ENABLEOPENSERVER":            "true",
			"MM_SERVICESETTINGS_ENABLELOCALMODE":          "true",
			"MM_SERVICESETTINGS_LOCALMODESOCKETLOCATION":  "/var/tmp/mattermost_local.socket",
			"MM_SERVICESETTINGS_ENABLEAPICHANNELDELETION": "true",
			// Disable features that might slow down startup
			"MM_PLUGINSETTINGS_ENABLE":        "false",
			"MM_PLUGINSETTINGS_ENABLEUPLOADS": "false",
			"MM_LOGSETTINGS_CONSOLELEVEL":     "ERROR",
			"MM_LOGSETTINGS_ENABLEFILE":       "false",
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(nat.Port(mattermostPort)),
			wait.ForHTTP("/api/v4/system/ping").
				WithPort(nat.Port(mattermostPort)).
				WithStatusCodeMatcher(func(status int) bool {
					return status == 200
				}),
		).WithDeadline(mattermostStartupTimeout),
	}

	mattermostContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", nil, errors.Wrap(err, "failed to start mattermost container")
	}

	tearDown := func(ctx context.Context) error {
		return mattermostContainer.Terminate(ctx)
	}

	host, err := mattermostContainer.Host(ctx)
	if err != nil {
		_ = mattermostContainer.Terminate(ctx)
		return nil, "", nil, errors.Wrap(err, "cannot get mattermost host")
	}

	port, err := mattermostContainer.MappedPort(ctx, nat.Port(mattermostPort))
	if err != nil {
		_ = mattermostContainer.Terminate(ctx)
		return nil, "", nil, errors.Wrap(err, "cannot get mattermost port")
	}

	siteURL := fmt.Sprintf("http://%s:%s", host, port.Port())

	return mattermostContainer, siteURL, tearDown, nil
}
