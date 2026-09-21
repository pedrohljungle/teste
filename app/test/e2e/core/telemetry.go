//go:build e2e

package core

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	apimetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/fx"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// telemetry captures what the application reports about itself, in memory: its metrics and its log
// lines. The suite runs with the exporters off, and reading the metrics and the logs is how a
// scenario asserts that the numbers and the lines an operator relies on are really produced.
//
// It is shared by every instance the suite starts, so what one publisher counts and what the server
// counts land in the same place, as they would in the same backend.
type telemetry struct {
	reader  *metric.ManualReader
	logs    *observer.ObservedLogs
	logCore zapcore.Core

	mu sync.Mutex
}

func newTelemetry() *telemetry {
	core, logs := observer.New(zapcore.DebugLevel)
	return &telemetry{reader: metric.NewManualReader(), logs: logs, logCore: core}
}

// options replaces the meter provider with one that a reader can be asked, and tees the logger into
// the recording core, so everything still goes where it went before as well.
func (t *telemetry) options() fx.Option {
	provider := metric.NewMeterProvider(metric.WithReader(t.reader))
	return fx.Options(
		fx.Decorate(func(apimetric.MeterProvider) apimetric.MeterProvider { return provider }),
		fx.Decorate(func(log *zap.Logger) *zap.Logger {
			return zap.New(zapcore.NewTee(log.Core(), t.logCore))
		}),
	)
}

// Metric is the sum of a counter, or the number of observations of a histogram, over the data points
// whose attributes include every pair in want. Reading it twice and subtracting is how a scenario
// asks "did this operation count".
func (s *Stack) Metric(t *testing.T, name string, want map[string]string) float64 {
	t.Helper()

	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()

	var collected metricdata.ResourceMetrics
	if err := s.telemetry.reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("collect the metrics: %v", err)
	}

	var total float64
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes.ToSlice(), want) {
						total += float64(point.Value)
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					if matches(point.Attributes.ToSlice(), want) {
						total += float64(point.Count)
					}
				}
			}
		}
	}
	return total
}

// LoggedLine is one line the application logged, with all its fields.
type LoggedLine struct {
	Message string
	Level   zapcore.Level
	Fields  map[string]any
}

// Logs is every line logged so far.
func (s *Stack) Logs() []LoggedLine {
	entries := s.telemetry.logs.All()
	lines := make([]LoggedLine, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, LoggedLine{Message: entry.Message, Level: entry.Level, Fields: entry.ContextMap()})
	}
	return lines
}

func matches(attributes []attribute.KeyValue, want map[string]string) bool {
	for key, value := range want {
		found := false
		for _, attribute := range attributes {
			if string(attribute.Key) == key && attribute.Value.AsString() == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
