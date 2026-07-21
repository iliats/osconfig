package poll

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUntilChecksImmediately(t *testing.T) {
	attempts := 0
	err := Until(context.Background(), time.Hour, "ready", func(context.Context) (string, bool, error) {
		attempts++
		return "ready", true, nil
	})
	if err != nil || attempts != 1 {
		t.Fatalf("Until() error=%v attempts=%d", err, attempts)
	}
}

func TestUntilReportsLastObservation(t *testing.T) {
	cause := errors.New("phase deadline")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	err := Until(ctx, time.Second, "inventory", func(context.Context) (string, bool, error) {
		return "NotFound inventory-1", false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "NotFound inventory-1") || !errors.Is(err, cause) {
		t.Fatalf("Until() error = %v", err)
	}
}
