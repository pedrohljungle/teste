// Package auth is the Keycloak adapter: it validates the tokens the API receives and obtains
// the service account token the worker uses.
package auth

import "go.uber.org/fx"

// ServerModule is wired only by the server entrypoint, which is the one that receives tokens.
var ServerModule = fx.Module("auth-server",
	fx.Provide(
		NewVerifier,
		func(v *Verifier) TokenVerifier { return v },
	),
)

// WorkerModule is wired only by the worker entrypoint, which authenticates as a machine.
var WorkerModule = fx.Module("auth-worker",
	fx.Provide(NewServiceAccount),
)
