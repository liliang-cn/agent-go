// Package main wires the OpenTelemetry bridge to stdout exporters, so a run's
// spans and metrics are visible without a collector.
//
// Point the same Observer at an OTLP TracerProvider (or use otelobserver.Phoenix)
// and the identical spans reach Arize Phoenix, Jaeger or anything else that
// speaks OTLP. The MeterProvider is optional: without WithMeterProvider the
// bridge only traces.
//
// Usage:
//
//	go run ./examples/otel
//
// For the cross-check version — in-memory exporters, a printed span tree, and
// every counter compared against the run's own ExecutionResult.Usage and
// EstimatedCostUSD — see examples/otel-probe.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/otelobserver"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	traceExp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		log.Fatalf("trace exporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(traceExp)))

	metricExp, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
	if err != nil {
		log.Fatalf("metric exporter: %v", err)
	}
	// A short interval so a demo run prints something before it exits; a real
	// deployment leaves this at the default and lets the reader flush on
	// Shutdown.
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(
		sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(15*time.Second)),
	))

	obs := otelobserver.New(tp, otelobserver.WithMeterProvider(mp))

	svc, err := agent.New("otel-demo").
		WithObserver(obs).
		Build()
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer svc.Close()

	answer, err := svc.Ask(ctx, "In one sentence, what is OpenTelemetry?")
	if err != nil {
		log.Printf("ask: %v", err)
	} else {
		fmt.Fprintln(os.Stdout, "answer:", answer)
	}

	// Shutdown order matters: the Observer first, so any root span whose task
	// never checkpointed is ended and therefore exportable, then the
	// providers, which flush.
	if err := obs.Shutdown(ctx); err != nil {
		log.Printf("observer shutdown: %v", err)
	}
	if err := tp.Shutdown(ctx); err != nil {
		log.Printf("tracer shutdown: %v", err)
	}
	if err := mp.Shutdown(ctx); err != nil {
		log.Printf("meter shutdown: %v", err)
	}
}
