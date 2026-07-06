package otelobserver

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// DefaultPhoenixEndpoint is Phoenix's OTLP/HTTP collector for traces.
const DefaultPhoenixEndpoint = "http://localhost:6006/v1/traces"

// phoenixConfig holds resolved Phoenix wiring options.
type phoenixConfig struct {
	endpoint    string
	serviceName string
	batch       bool
	obsOpts     []Option
}

// PhoenixOption configures the Phoenix convenience wiring.
type PhoenixOption func(*phoenixConfig)

// WithEndpoint overrides the OTLP/HTTP traces endpoint (full URL, including
// /v1/traces). Takes precedence over PHOENIX_COLLECTOR_ENDPOINT.
func WithEndpoint(url string) PhoenixOption {
	return func(c *phoenixConfig) {
		if url != "" {
			c.endpoint = url
		}
	}
}

// WithPhoenixServiceName sets the Resource service.name (default "agent-go").
func WithPhoenixServiceName(name string) PhoenixOption {
	return func(c *phoenixConfig) {
		if name != "" {
			c.serviceName = name
		}
	}
}

// WithSimpleSpanProcessor uses a synchronous span processor instead of the
// default batch processor. Handy in tests / short-lived programs.
func WithSimpleSpanProcessor() PhoenixOption {
	return func(c *phoenixConfig) { c.batch = false }
}

// WithObserverOptions forwards Options to the underlying Observer (e.g.
// WithRootKind).
func WithObserverOptions(opts ...Option) PhoenixOption {
	return func(c *phoenixConfig) { c.obsOpts = append(c.obsOpts, opts...) }
}

// Phoenix builds an OTLP/HTTP exporter and an SDK TracerProvider pointed at
// Arize Phoenix, returning an Observer wired to it plus a shutdown func that
// flushes the exporter AND shuts down the provider. If Phoenix is down the
// (batch) exporter buffers/drops silently — the program does not crash.
//
// Endpoint resolution order: WithEndpoint > PHOENIX_COLLECTOR_ENDPOINT env >
// DefaultPhoenixEndpoint. The endpoint is treated as a full URL.
func Phoenix(ctx context.Context, opts ...PhoenixOption) (*Observer, func(context.Context) error, error) {
	cfg := &phoenixConfig{
		endpoint:    DefaultPhoenixEndpoint,
		serviceName: "agent-go",
		batch:       true,
	}
	if env := os.Getenv("PHOENIX_COLLECTOR_ENDPOINT"); env != "" {
		cfg.endpoint = env
	}
	for _, opt := range opts {
		opt(cfg)
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.endpoint),
	)
	if err != nil {
		return nil, nil, err
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.serviceName),
		),
	)
	if err != nil {
		// Fall back to a bare resource rather than failing wiring.
		res = resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(cfg.serviceName))
	}

	var sp sdktrace.SpanProcessor
	if cfg.batch {
		sp = sdktrace.NewBatchSpanProcessor(exporter)
	} else {
		sp = sdktrace.NewSimpleSpanProcessor(exporter)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sp),
		sdktrace.WithResource(res),
	)

	obs := New(tp, cfg.obsOpts...)

	shutdown := func(ctx context.Context) error {
		// End dangling roots, flush, then tear down the provider.
		_ = obs.Shutdown(ctx)
		if err := tp.ForceFlush(ctx); err != nil {
			// best-effort flush; continue to shutdown
			_ = tp.Shutdown(ctx)
			return err
		}
		return tp.Shutdown(ctx)
	}
	return obs, shutdown, nil
}
