package middleware

import (
	"github.com/labstack/echo/v4"

	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// Telemetry instruments every request.
type Telemetry struct {
	obs *observability.Observer
}

// NewTelemetry builds the middleware.
func NewTelemetry(obs *observability.Observer) *Telemetry {
	return &Telemetry{obs: obs}
}

// TraceRequest opens the handler layer span and installs the per-request error marker, which
// is what keeps one error from being logged once per layer.
//
// It is applied with e.Use, after the OpenTelemetry middleware that already created the server
// span from the incoming traceparent.
func (m *Telemetry) TraceRequest(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		req := c.Request()
		ctx := observability.WithErrorTrail(req.Context())
		// The request id is the correlation id: the events the request causes carry it, so a
		// provider's complaint about one call can be followed to everything it produced.
		ctx = observability.WithCorrelationID(ctx, c.Response().Header().Get(echo.HeaderXRequestID))

		ctx, end := m.obs.Start(ctx, observability.LayerHandler, req.Method+" "+c.Path(),
			observability.String("http.route", c.Path()),
			observability.String("http.method", req.Method),
		)

		c.SetRequest(req.WithContext(ctx))
		err := next(c)

		end(err)
		return err
	}
}
