package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// ServiceAccount is the worker identity in the same realm. It has no user in front of it, so
// it authenticates with client_credentials.
type ServiceAccount struct {
	source oauth2.TokenSource
	obs    *observability.Observer
}

// NewServiceAccount builds a cached token source, so a busy worker does not open a new
// conversation with the IDP per job.
func NewServiceAccount(cfg config.Keycloak, obs *observability.Observer) *ServiceAccount {
	conf := &clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL(),
	}
	return &ServiceAccount{
		source: oauth2.ReuseTokenSource(nil, conf.TokenSource(context.Background())),
		obs:    obs,
	}
}

// Token returns a valid access token, refreshing it when needed.
func (s *ServiceAccount) Token(ctx context.Context) (token string, err error) {
	// The context feeds the span only: oauth2.TokenSource takes no per-call context.
	_, end := s.obs.Start(ctx, observability.LayerGateway, "Keycloak.ServiceAccountToken")
	defer func() { end(err) }()

	tok, err := s.source.Token()
	if err != nil {
		return "", fmt.Errorf("obtain service account token: %w", err)
	}
	return tok.AccessToken, nil
}
