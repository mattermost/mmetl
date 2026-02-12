package testhelper

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/pkg/errors"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dbUser         = "mmuser"
	dbPassword     = "mostest"
	dbName         = "mattermost_test"
	mattermostPort = "8065/tcp"
	postgresAlias  = "postgres"
)

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
				WithStartupTimeout(30*time.Second),
		),
		network.WithNetwork([]string{postgresAlias}, &testcontainers.DockerNetwork{Name: networkName}),
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

	req := testcontainers.ContainerRequest{
		Image:        "mattermost/mattermost-team-edition:latest",
		ExposedPorts: []string{mattermostPort},
		Networks:     []string{networkName},
		Env: map[string]string{
			"MM_SQLSETTINGS_DRIVERNAME":                   "postgres",
			"MM_SQLSETTINGS_DATASOURCE":                   postgresConnStr,
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
		).WithDeadline(120 * time.Second),
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
		return nil, "", nil, errors.Wrap(err, "cannot get mattermost host")
	}

	port, err := mattermostContainer.MappedPort(ctx, nat.Port(mattermostPort))
	if err != nil {
		return nil, "", nil, errors.Wrap(err, "cannot get mattermost port")
	}

	siteURL := fmt.Sprintf("http://%s:%s", host, port.Port())

	return mattermostContainer, siteURL, tearDown, nil
}
