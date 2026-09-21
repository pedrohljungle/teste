//go:build e2e

package core

import (
	"context"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// localstackAccountID is the fixed account LocalStack uses for every resource.
const localstackAccountID = "000000000000"

// infra is the set of real dependencies the suite runs against. Nothing here is a fake: the
// point of this suite is to exercise the adapters, which a unit test never does.
type infra struct {
	databaseURL string
	redisURL    string
	awsEndpoint string
	queueURL    string
	dlqURL      string
	eventsURL   string
	keycloakURL string

	terminate func(context.Context)
}

func startInfra(ctx context.Context) (*infra, error) {
	var started []testcontainers.Container
	stop := func(ctx context.Context) {
		for _, c := range started {
			_ = c.Terminate(ctx)
		}
	}
	fail := func(err error) (*infra, error) {
		stop(ctx)
		return nil, err
	}

	postgres, postgresURL, err := startPostgres(ctx)
	if err != nil {
		return fail(fmt.Errorf("start postgres: %w", err))
	}
	started = append(started, postgres)

	redis, redisURL, err := startRedis(ctx)
	if err != nil {
		return fail(fmt.Errorf("start redis: %w", err))
	}
	started = append(started, redis)

	localstack, awsEndpoint, err := startLocalstack(ctx)
	if err != nil {
		return fail(fmt.Errorf("start localstack: %w", err))
	}
	started = append(started, localstack)

	keycloak, keycloakURL, err := startKeycloak(ctx)
	if err != nil {
		return fail(fmt.Errorf("start keycloak: %w", err))
	}
	started = append(started, keycloak)

	return &infra{
		databaseURL: postgresURL,
		redisURL:    redisURL,
		awsEndpoint: awsEndpoint,
		queueURL:    fmt.Sprintf("%s/%s/wager-transactions.fifo", awsEndpoint, localstackAccountID),
		dlqURL:      fmt.Sprintf("%s/%s/wager-transactions-dlq.fifo", awsEndpoint, localstackAccountID),
		eventsURL:   fmt.Sprintf("%s/%s/wager-events.fifo", awsEndpoint, localstackAccountID),
		keycloakURL: keycloakURL,
		terminate:   stop,
	}, nil
}

func startPostgres(ctx context.Context) (testcontainers.Container, string, error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:18-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "postgres",
				"POSTGRES_PASSWORD": "postgres",
				"POSTGRES_DB":       "pedro_test",
			},
			// Postgres starts, shuts down to finish initdb and starts again, so the log
			// line has to be seen twice before the server is really accepting connections.
			WaitingFor: wait.ForAll(
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
				wait.ForListeningPort("5432/tcp"),
			).WithDeadline(2 * time.Minute),
		},
	})
	if err != nil {
		return nil, "", err
	}

	endpoint, err := container.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return nil, "", err
	}
	return container, fmt.Sprintf("postgres://postgres:postgres@%s/pedro_test?sslmode=disable", endpoint), nil
}

func startRedis(ctx context.Context) (testcontainers.Container, string, error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:8-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(time.Minute),
		},
	})
	if err != nil {
		return nil, "", err
	}

	endpoint, err := container.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		return nil, "", err
	}
	return container, fmt.Sprintf("redis://%s/0", endpoint), nil
}

func startLocalstack(ctx context.Context) (testcontainers.Container, string, error) {
	script, err := repoFile("docker/localstack/init-queues.sh")
	if err != nil {
		return nil, "", err
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "localstack/localstack:3.7",
			ExposedPorts: []string{"4566/tcp"},
			Env: map[string]string{
				"SERVICES":       "sqs",
				"DEFAULT_REGION": "us-east-1",
			},
			// The very same init script the compose stack uses, so the suite runs against
			// the queue topology that is actually configured, dead letter queue included.
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      script,
				ContainerFilePath: "/etc/localstack/init/ready.d/init-queues.sh",
				FileMode:          0o755,
			}},
			WaitingFor: wait.ForLog("queues ready").WithStartupTimeout(3 * time.Minute),
		},
	})
	if err != nil {
		return nil, "", err
	}

	endpoint, err := container.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		return nil, "", err
	}
	return container, endpoint, nil
}

func startKeycloak(ctx context.Context) (testcontainers.Container, string, error) {
	realm, err := repoFile("docker/keycloak/realm-pedro-test.json")
	if err != nil {
		return nil, "", err
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true,
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "quay.io/keycloak/keycloak:26.7",
			Cmd:          []string{"start-dev", "--import-realm"},
			ExposedPorts: []string{"8080/tcp"},
			Env: map[string]string{
				"KC_BOOTSTRAP_ADMIN_USERNAME": "admin",
				"KC_BOOTSTRAP_ADMIN_PASSWORD": "admin",
				// The hostname is deliberately NOT pinned here: the container port is
				// random, and letting Keycloak derive the issuer from the request Host is
				// what keeps the token iss equal to the address the suite calls.
				"KC_HOSTNAME_STRICT": "false",
				"KC_HTTP_ENABLED":    "true",
			},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      realm,
				ContainerFilePath: "/opt/keycloak/data/import/realm-pedro-test.json",
				FileMode:          0o644,
			}},
			// Waiting on the realm discovery document, and not on the port, is what proves
			// the import finished.
			WaitingFor: wait.ForHTTP("/realms/pedro-test/.well-known/openid-configuration").
				WithPort("8080/tcp").
				WithStartupTimeout(4 * time.Minute),
		},
	})
	if err != nil {
		return nil, "", err
	}

	endpoint, err := container.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		return nil, "", err
	}
	return container, endpoint, nil
}
