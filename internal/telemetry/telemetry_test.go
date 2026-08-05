package telemetry

import (
	"context"
	"testing"
)

// TestInitDisabledIsNoop verifies that with both signals off, Init sets up no
// exporters, never fails, and returns a shutdown func that is safe to call.
func TestInitDisabledIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{})
	if err != nil {
		t.Fatalf("Init(disabled) returned error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned a nil shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
}
