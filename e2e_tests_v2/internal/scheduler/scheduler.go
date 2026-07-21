package scheduler

import (
	"context"
	"fmt"
	"sync"

	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
)

// Scheduler provides context-aware bounded project/zone admission. CI shards
// should receive disjoint target sets when cross-process isolation is needed.
type Scheduler struct {
	available chan config.Target
}

// New creates one token per configured target capacity unit.
func New(targets []config.Target) (*Scheduler, error) {
	total := 0
	for _, target := range targets {
		if target.Project == "" || target.Zone == "" || target.Capacity <= 0 {
			return nil, fmt.Errorf("invalid scheduler target: %+v", target)
		}
		total += target.Capacity
	}
	if total == 0 {
		return nil, fmt.Errorf("scheduler has no capacity")
	}
	available := make(chan config.Target, total)
	for _, target := range targets {
		for i := 0; i < target.Capacity; i++ {
			available <- target
		}
	}
	return &Scheduler{available: available}, nil
}

// Lease is an exclusive capacity token. Release is idempotent.
type Lease struct {
	Target  config.Target
	release func()
	once    sync.Once
}

// Release returns capacity to the scheduler.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(l.release)
}

// Acquire waits for capacity or returns the caller's cancellation cause.
func (s *Scheduler) Acquire(ctx context.Context) (*Lease, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("acquire project/zone lease: %w", context.Cause(ctx))
	case target := <-s.available:
		return &Lease{
			Target: target,
			release: func() {
				s.available <- target
			},
		}, nil
	}
}
