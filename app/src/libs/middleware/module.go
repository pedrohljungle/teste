// Package middleware holds the HTTP middlewares: the cross-cutting concerns a request passes
// through, regardless of domain.
//
// It lives in libs/ and not under handlers/ because it is not delivery of anything — it is
// plumbing that delivery uses. A handler answers a route; a middleware decides whether the
// request gets to a handler at all, and what is recorded about it.
package middleware

import "go.uber.org/fx"

// Module is wired by the HTTP entrypoint. The worker probe serves no authenticated route and
// does not need it.
var Module = fx.Module("middleware",
	fx.Provide(
		NewAuth,
		NewTelemetry,
	),
)
