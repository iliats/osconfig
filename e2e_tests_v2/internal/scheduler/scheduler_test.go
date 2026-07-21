package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
)

func TestAcquireHonorsCapacityAndCancellation(t *testing.T) {
	s, err := New([]config.Target{{Project: "project", Zone: "zone", Capacity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx); err == nil {
		t.Fatal("second Acquire() succeeded while capacity was exhausted")
	}

	first.Release()
	first.Release()
	second, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire() after Release(): %v", err)
	}
	second.Release()
}
