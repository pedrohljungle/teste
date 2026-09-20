package observability

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
)

// Module is identical in every entrypoint. What differs is the appinfo.App each one supplies,
// which is what segregates telemetry per application.
var Module = fx.Module("observability",
	fx.Provide(
		NewZapLogger,
		NewTracerProvider,
		NewMeterProvider,
		NewTracer,
		NewMeter,
		NewObserver,
	),
	// Routes fx boot events into the same structured log as everything else.
	fx.WithLogger(func(log *zap.Logger) fxevent.Logger {
		return &fxevent.ZapLogger{Logger: log.Named("fx")}
	}),
)

// NewZapLogger builds the application logger: JSON in production, console in development.
func NewZapLogger(cfg config.Config, app appinfo.App) (*zap.Logger, error) {
	var zcfg zap.Config
	if cfg.IsDevelopment() {
		zcfg = zap.NewDevelopmentConfig()
		zcfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		zcfg = zap.NewProductionConfig()
		zcfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	}
	// InitialFields also reaches lines that do not go through the Observer, such as the ones
	// from fx and Echo, so no line leaves without naming its application.
	zcfg.InitialFields = map[string]any{
		"service": app.Name,
		"env":     cfg.Env,
	}

	log, err := zcfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	return log, nil
}

// NewTracerProvider builds the trace provider. Without an endpoint it returns a no-op
// provider: instrumented code keeps running, spans just never leave the process.
func NewTracerProvider(lc fx.Lifecycle, cfg config.Telemetry, app appinfo.App, log *zap.Logger) (trace.TracerProvider, error) {
	// The propagator is needed even with exporting off: it carries the traceparent that
	// stitches API, queue and worker into one trace.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if cfg.OTLPEndpoint == "" {
		log.Warn("OTEL_EXPORTER_OTLP_ENDPOINT is empty: traces will not be exported")
		provider := tracenoop.NewTracerProvider()
		otel.SetTracerProvider(provider)
		return provider, nil
	}

	exporter, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(normalizeEndpoint(cfg.OTLPEndpoint)),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	res, err := newResource(app)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(provider)

	// Without this flush the batcher drops the spans still in memory, which are exactly the
	// ones from the request that brought the process down.
	lc.Append(fx.Hook{
		OnStop: provider.Shutdown,
	})
	return provider, nil
}

// NewMeterProvider builds the metric provider, with the same no-op fallback as traces.
func NewMeterProvider(lc fx.Lifecycle, cfg config.Telemetry, app appinfo.App) (metric.MeterProvider, error) {
	if cfg.OTLPEndpoint == "" {
		provider := metricnoop.NewMeterProvider()
		otel.SetMeterProvider(provider)
		return provider, nil
	}

	exporter, err := otlpmetricgrpc.New(context.Background(),
		otlpmetricgrpc.WithEndpoint(normalizeEndpoint(cfg.OTLPEndpoint)),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create otlp metric exporter: %w", err)
	}

	res, err := newResource(app)
	if err != nil {
		return nil, err
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(cfg.MetricInterval),
		)),
	)
	otel.SetMeterProvider(provider)

	lc.Append(fx.Hook{
		OnStop: provider.Shutdown,
	})
	return provider, nil
}

// NewTracer returns the application tracer.
func NewTracer(provider trace.TracerProvider, app appinfo.App) trace.Tracer {
	return provider.Tracer(app.Name)
}

// NewMeter returns the application meter.
func NewMeter(provider metric.MeterProvider, app appinfo.App) metric.Meter {
	return provider.Meter(app.Name)
}

func newResource(app appinfo.App) (*resource.Resource, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(app.Name),
			semconv.ServiceVersion(app.Version),
			semconv.ServiceNamespace("pedro-test"),
			attribute.String(attrRole, string(app.Role)),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build telemetry resource: %w", err)
	}
	return res, nil
}

// normalizeEndpoint accepts both host:port and http://host:port, because the OTLP environment
// variable is commonly written in the second form while the gRPC exporter wants the first.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	return strings.TrimSuffix(endpoint, "/")
}
