package fleettrace

import (
	"context"
	"testing"
)

func TestConfigureIsInertWhenEndpointIsAbsent(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	shutdown, err := Configure(context.Background(), "service", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureRejectsEndpointWidening(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://10.200.0.7:4318/v1/traces")
	if _, err := Configure(context.Background(), "service", "v1.0.0"); err == nil {
		t.Fatal("non-loopback trace endpoint was accepted")
	}
}
