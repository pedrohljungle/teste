package outbox

import (
	"context"
	"time"

	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/cronjob"
)

var _ cronjob.Task = (*CronjobHandler)(nil)

// CronjobHandler is the outbox publisher as a recurring task: every tick hands the service the
// batch to drain. It holds no rule; what to publish, and what to do with a failure, is the
// service's.
type CronjobHandler struct {
	service  outboxiface.Service
	interval time.Duration
}

// NewCronjobHandler builds the handler.
func NewCronjobHandler(service outboxiface.Service, cfg config.Outbox) *CronjobHandler {
	return &CronjobHandler{service: service, interval: cfg.PollInterval}
}

// PrepareCronjob registers the outbox publisher on the cronjob runtime, mirroring how a domain
// registers its queue handler with PrepareWorker.
func PrepareCronjob(runner *cronjob.Runner, h *CronjobHandler) {
	runner.Register(h)
}

// Name is what the task appears as on spans and logs.
func (h *CronjobHandler) Name() string { return "outbox-publisher" }

// Interval is how long to wait between ticks when the outbox was empty.
func (h *CronjobHandler) Interval() time.Duration { return h.interval }

// Run publishes what is due.
func (h *CronjobHandler) Run(ctx context.Context) (int, error) {
	return h.service.PublishDue(ctx)
}
