// Package config loads application settings from the environment (and .env in development).
package config

import (
	"errors"
	"strings"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/fx"
)

// Module provides the configuration. Sub-blocks are provided separately so each component
// asks only for the slice it needs.
var Module = fx.Module("config",
	fx.Provide(
		Load,
		func(c Config) Keycloak { return c.Keycloak },
		func(c Config) Worker { return c.Worker },
		func(c Config) Telemetry { return c.Telemetry },
		func(c Config) AWS { return c.AWS },
	),
)

// Config is the whole application configuration, shared by every entrypoint.
type Config struct {
	Env         string
	Port        string
	DatabaseURL string
	// DatabasePassword is optional and, when set, replaces whatever the URL carries. It
	// exists because a managed secret is injected on its own: in ECS the URL comes from the
	// task definition and the password from Secrets Manager, so neither the plan nor the
	// Terraform state ever holds a credential.
	DatabasePassword string
	RedisURL         string
	Keycloak         Keycloak
	Worker           Worker
	Telemetry        Telemetry
	AWS              AWS
	// DocsEnabled registers the API documentation and the OpenAPI document. It is off by
	// default: the documentation describes every route and the shape of every payload, which
	// is a map nobody needs handed to them in production.
	DocsEnabled bool
}

// Keycloak holds the IDP settings.
type Keycloak struct {
	// Issuer is the external realm address, the one Keycloak writes into the token iss
	// claim and therefore the one verification compares against.
	Issuer string
	// InternalURL is how this application reaches the realm. Empty means "same as Issuer".
	// The two differ whenever the token is obtained from outside the network the
	// application runs in, which is the usual case in Compose and in Kubernetes.
	InternalURL  string
	Audience     string
	ClientID     string
	ClientSecret string
}

// DiscoveryURL is the realm address this application can actually reach, used to load the
// OIDC metadata on start.
func (k Keycloak) DiscoveryURL() string {
	return k.reachableURL()
}

// JWKSURL is where the realm publishes its signing keys.
//
// It is derived from the reachable address on purpose, instead of being read from the
// discovery document: Keycloak advertises the external jwks_uri there, which is exactly the
// address this application cannot reach when the two differ.
func (k Keycloak) JWKSURL() string {
	return k.reachableURL() + "/protocol/openid-connect/certs"
}

// TokenURL is the client_credentials endpoint used by the worker.
func (k Keycloak) TokenURL() string {
	return k.reachableURL() + "/protocol/openid-connect/token"
}

// PublicTokenURL is the token endpoint as a BROWSER reaches it, derived from the issuer rather
// than from the reachable address. It goes into the OpenAPI document, and the two differ
// exactly when the documentation page is useful: pointing the Authorize button at the internal
// hostname would make it fail for everyone outside the network.
func (k Keycloak) PublicTokenURL() string {
	return k.Issuer + "/protocol/openid-connect/token"
}

// PublicClientID is the client the documentation page authenticates with.
func (k Keycloak) PublicClientID() string {
	return k.Audience
}

func (k Keycloak) reachableURL() string {
	if k.InternalURL != "" {
		return k.InternalURL
	}
	return k.Issuer
}

// Worker holds the job consumer settings.
type Worker struct {
	// QueueURL is the SQS queue the server publishes to and the worker consumes from.
	QueueURL string
	// PollTimeout is the SQS long polling wait. It is also the worst case delay between
	// SIGTERM and the loop noticing it must stop, and SQS caps it at 20s.
	PollTimeout time.Duration
	// VisibilityTimeout is how long a message stays hidden after delivery. It must exceed
	// the slowest handler, otherwise a still-running message is delivered again.
	VisibilityTimeout int32
	Concurrency       int
}

// Name is the queue name, derived from the URL. It is what appears on spans and logs, because
// the full URL carries the account id and adds nothing to a reader.
func (w Worker) Name() string {
	if idx := strings.LastIndex(w.QueueURL, "/"); idx >= 0 {
		return w.QueueURL[idx+1:]
	}
	return w.QueueURL
}

// AWS holds the SDK settings. Endpoint is only set outside AWS, to point the SDK at a local
// emulator; empty means the real service.
type AWS struct {
	Region   string
	Endpoint string
}

// Telemetry holds the OpenTelemetry settings. An empty endpoint turns exporting off without
// changing any instrumented code path.
type Telemetry struct {
	OTLPEndpoint string
	SampleRatio  float64
	// MetricInterval is how often metrics are pushed. The SDK default is 60s, which is right
	// in production and far too slow when someone is watching a dashboard to see whether a
	// change worked.
	MetricInterval time.Duration
}

// Load reads the environment and fails when a required setting is missing.
func Load() (Config, error) {
	v := viper.New()
	v.SetConfigFile(".env")
	v.SetConfigType("dotenv")
	_ = v.ReadInConfig() // .env is optional; environment variables win
	v.AutomaticEnv()

	v.SetDefault("PORT", "3000")
	v.SetDefault("APP_ENV", "development")
	v.SetDefault("KEYCLOAK_AUDIENCE", "pedro-test-api")
	v.SetDefault("WORKER_POLL_TIMEOUT", "20s")
	v.SetDefault("WORKER_VISIBILITY_TIMEOUT", 60)
	v.SetDefault("WORKER_CONCURRENCY", 4)
	v.SetDefault("AWS_REGION", "us-east-1")
	v.SetDefault("OTEL_TRACES_SAMPLER_ARG", 1.0)
	v.SetDefault("OTEL_METRIC_EXPORT_INTERVAL", "60s")
	v.SetDefault("DOCS_ENABLED", false)

	cfg := Config{
		Env:              v.GetString("APP_ENV"),
		Port:             v.GetString("PORT"),
		DatabaseURL:      v.GetString("DATABASE_URL"),
		DatabasePassword: v.GetString("DATABASE_PASSWORD"),
		RedisURL:         v.GetString("REDIS_URL"),
		Keycloak: Keycloak{
			Issuer:       v.GetString("KEYCLOAK_ISSUER"),
			InternalURL:  v.GetString("KEYCLOAK_INTERNAL_URL"),
			Audience:     v.GetString("KEYCLOAK_AUDIENCE"),
			ClientID:     v.GetString("KEYCLOAK_CLIENT_ID"),
			ClientSecret: v.GetString("KEYCLOAK_CLIENT_SECRET"),
		},
		Worker: Worker{
			QueueURL:          v.GetString("SQS_QUEUE_URL"),
			PollTimeout:       v.GetDuration("WORKER_POLL_TIMEOUT"),
			VisibilityTimeout: v.GetInt32("WORKER_VISIBILITY_TIMEOUT"),
			Concurrency:       v.GetInt("WORKER_CONCURRENCY"),
		},
		DocsEnabled: v.GetBool("DOCS_ENABLED"),
		AWS: AWS{
			Region:   v.GetString("AWS_REGION"),
			Endpoint: v.GetString("AWS_ENDPOINT_URL"),
		},
		Telemetry: Telemetry{
			OTLPEndpoint:   v.GetString("OTEL_EXPORTER_OTLP_ENDPOINT"),
			SampleRatio:    v.GetFloat64("OTEL_TRACES_SAMPLER_ARG"),
			MetricInterval: v.GetDuration("OTEL_METRIC_EXPORT_INTERVAL"),
		},
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	// Redis backs the listing cache. The queue lives in SQS.
	if c.RedisURL == "" {
		return errors.New("REDIS_URL is required (cache)")
	}
	if c.Worker.QueueURL == "" {
		return errors.New("SQS_QUEUE_URL is required")
	}
	if c.Keycloak.Issuer == "" {
		return errors.New("KEYCLOAK_ISSUER is required")
	}
	if c.Worker.Concurrency < 1 {
		return errors.New("WORKER_CONCURRENCY must be at least 1")
	}
	return nil
}

// Addr is the listen address of the HTTP server.
func (c Config) Addr() string {
	return ":" + c.Port
}

// IsDevelopment drives log formatting and other developer-facing defaults.
func (c Config) IsDevelopment() bool {
	return c.Env == "development"
}
