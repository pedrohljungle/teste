package reference

import (
	"context"
	"time"

	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
)

// CronjobHandler is the resolution of pending references as a recurring task: every tick hands the
// resolver the reversals that are due. It holds no rule; when a reversal is applied, refused or made
// to wait longer is the service's.
type CronjobHandler struct {
	resolver wageringiface.ReferenceResolver
	interval time.Duration
}

// NewCronjobHandler builds the handler.
func NewCronjobHandler(resolver wageringiface.ReferenceResolver, cfg config.Reference) *CronjobHandler {
	return &CronjobHandler{resolver: resolver, interval: cfg.PollInterval}
}

// Name is what the task appears as on spans and logs.
func (h *CronjobHandler) Name() string { return "reference-resolver" }

// Interval is how long to wait between ticks when no reversal was due.
func (h *CronjobHandler) Interval() time.Duration { return h.interval }

// Run resolves what is due.
func (h *CronjobHandler) Run(ctx context.Context) (int, error) {
	return h.resolver.ResolvePending(ctx)
}
