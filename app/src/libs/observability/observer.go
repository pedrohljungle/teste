// Package observability exposes tracing, metrics and logging behind a single type.
//
// Every layer instruments through Observer.Start, whose closer takes the operation error. A
// non-nil error always becomes a recorded span error, a failure metric and an error log, so
// there is no code path where an error goes unreported.
package observability

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"

	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
)

// Layer names the layer an operation belongs to, so a trace can tell how much time was spent
// in delivery, in business rules and in storage.
type Layer string

const (
	LayerHandler    Layer = "handler"
	LayerService    Layer = "service"
	LayerRepository Layer = "repository"
	LayerGateway    Layer = "gateway"
)

const (
	attrLayer     = "app.layer"
	attrOperation = "app.operation"
	attrStatus    = "app.status"
	attrRole      = "app.role"
)

// Field is a structured log field. Layers use this alias instead of importing zap directly,
// which keeps the logging library replaceable from a single package.
type Field = zap.Field

// Field constructors re-exported for the layers.
var (
	String   = zap.String
	Int      = zap.Int
	Int64    = zap.Int64
	Bool     = zap.Bool
	Float64  = zap.Float64
	Duration = zap.Duration
	Any      = zap.Any
)

// Observer is the instrumentation handle injected into every layer.
type Observer struct {
	log      *zap.Logger
	tracer   trace.Tracer
	duration metric.Int64Histogram
	failures metric.Int64Counter
	app      appinfo.App
}

// NewObserver builds an Observer over an already configured tracer and meter.
func NewObserver(app appinfo.App, log *zap.Logger, tracer trace.Tracer, meter metric.Meter) (*Observer, error) {
	duration, err := meter.Int64Histogram(
		"app.operation.duration",
		metric.WithDescription("operation duration, by layer"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}
	failures, err := meter.Int64Counter(
		"app.operation.failures",
		metric.WithDescription("operations that ended in error, by layer"),
	)
	if err != nil {
		return nil, err
	}
	return &Observer{
		log:      log.With(zap.String("app", app.Name), zap.String("role", string(app.Role))),
		tracer:   tracer,
		duration: duration,
		failures: failures,
		app:      app,
	}, nil
}

// NewNop returns an Observer that exports nothing. It serves code running outside a wired
// application, such as unit tests.
func NewNop() *Observer {
	obs, err := NewObserver(
		appinfo.App{Name: "nop", Role: appinfo.RoleServer},
		zap.NewNop(),
		tracenoop.NewTracerProvider().Tracer("nop"),
		metricnoop.NewMeterProvider().Meter("nop"),
	)
	if err != nil {
		panic(err)
	}
	return obs
}

// Zap returns the raw logger, for integrations that require a *zap.Logger.
func (o *Observer) Zap() *zap.Logger { return o.log }

// Start opens a span for the given layer and returns the child context and a closer.
//
// The closer must receive the operation error, which requires a named return and the long
// defer form:
//
//	func (s *TaskService) List(ctx context.Context) (tasks []entities.Task, err error) {
//	    ctx, end := s.obs.Start(ctx, observability.LayerService, "TaskService.List")
//	    defer func() { end(err) }()
//
// The short form, defer end(err), evaluates err while it is still nil.
func (o *Observer) Start(ctx context.Context, layer Layer, operation string, fields ...Field) (context.Context, func(error)) {
	ctx, span := o.tracer.Start(ctx, operation, trace.WithAttributes(
		attribute.String(attrLayer, string(layer)),
		attribute.String(attrOperation, operation),
		attribute.String(attrRole, string(o.app.Role)),
	))
	started := time.Now()

	return ctx, func(err error) {
		elapsed := time.Since(started)
		status := "ok"
		if err != nil {
			status = "error"
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			o.recordFailure(ctx, layer, operation, err, elapsed, fields)
		}
		o.duration.Record(ctx, elapsed.Milliseconds(), metric.WithAttributes(
			attribute.String(attrLayer, string(layer)),
			attribute.String(attrOperation, operation),
			attribute.String(attrStatus, status),
		))
		span.End()
	}
}

func (o *Observer) recordFailure(ctx context.Context, layer Layer, operation string, err error, elapsed time.Duration, fields []Field) {
	o.failures.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrLayer, string(layer)),
		attribute.String(attrOperation, operation),
	))
	if !shouldLog(ctx, err) {
		return
	}
	all := make([]Field, 0, len(fields)+4)
	all = append(all, fields...)
	all = append(all,
		zap.String(attrLayer, string(layer)),
		zap.String(attrOperation, operation),
		zap.Duration("elapsed", elapsed),
		zap.Error(err),
	)
	o.log.With(o.correlation(ctx)...).Error(operation+" failed", all...)
}

// Error reports an error that is deliberately not propagated, such as a degraded cache. It is
// the only sanctioned way to swallow an error.
func (o *Observer) Error(ctx context.Context, err error, msg string, fields ...Field) {
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.RecordError(err)
	}
	all := append(append([]Field{}, fields...), zap.Error(err))
	o.log.With(o.correlation(ctx)...).Error(msg, all...)
}

// Info logs at info level, correlated with the current span.
func (o *Observer) Info(ctx context.Context, msg string, fields ...Field) {
	o.log.With(o.correlation(ctx)...).Info(msg, fields...)
}

// Warn logs at warn level, correlated with the current span.
func (o *Observer) Warn(ctx context.Context, msg string, fields ...Field) {
	o.log.With(o.correlation(ctx)...).Warn(msg, fields...)
}

// Debug logs at debug level, correlated with the current span.
func (o *Observer) Debug(ctx context.Context, msg string, fields ...Field) {
	o.log.With(o.correlation(ctx)...).Debug(msg, fields...)
}

func (o *Observer) correlation(ctx context.Context) []Field {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []Field{
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	}
}

// An error raised in the repository crosses the service and the handler. It is recorded on
// every span, because that is what draws the path, but logged only once, by the innermost
// layer. The trail below is what makes that decision possible: it is a pointer kept in the
// context precisely because the information has to travel outwards, and a context value only
// travels inwards.
type errorTrail struct {
	mu     sync.Mutex
	logged []error
}

type errorTrailKey struct{}

// WithErrorTrail installs the per-unit-of-work marker that keeps a single error from being
// logged once per layer. It is installed once per HTTP request and once per job. A context
// without it falls back to logging every time.
func WithErrorTrail(ctx context.Context) context.Context {
	return context.WithValue(ctx, errorTrailKey{}, &errorTrail{})
}

func shouldLog(ctx context.Context, err error) bool {
	trail, ok := ctx.Value(errorTrailKey{}).(*errorTrail)
	if !ok {
		return true
	}
	trail.mu.Lock()
	defer trail.mu.Unlock()
	for _, seen := range trail.logged {
		// errors.Is, not ==: an upper layer usually wraps the error from the one below.
		if errors.Is(err, seen) {
			return false
		}
	}
	trail.logged = append(trail.logged, err)
	return true
}
