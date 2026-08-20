package fleettrace

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/telemetryattrs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

const expectedEndpoint = "http://127.0.0.1:4318/v1/traces"

type Shutdown func(context.Context) error

// Configure installs a process-wide provider only when the hardened service
// explicitly supplies the exact loopback endpoint. Operator subcommands and
// tests remain inert; a configured but widened endpoint fails closed.
func Configure(ctx context.Context, serviceName, serviceVersion string) (Shutdown, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	if endpoint != expectedEndpoint {
		return nil, fmt.Errorf("trace endpoint must remain %s", expectedEndpoint)
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.ServiceNamespace(telemetryattrs.ServiceNamespace),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(250*time.Millisecond)),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
