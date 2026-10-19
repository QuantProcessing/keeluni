package resident

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestPollKeepsLastGoodValueAndReportsStaleness(t *testing.T) {
	registry := NewRegistry()
	var now atomic.Int64
	now.Store(time.Unix(1000, 0).UnixNano())
	registry.now = func() time.Time { return time.Unix(0, now.Load()) }

	var calls atomic.Int32
	var mu sync.Mutex
	applied := []int{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, registry, Spec[int]{
		Dataset: "funding", Market: "okx:perp:USDT", Interval: 5 * time.Millisecond,
		Load: func(context.Context) (int, error) {
			call := int(calls.Add(1))
			if call >= 2 {
				return 0, errors.New("venue down")
			}
			return call, nil
		},
		Apply: func(value int) { mu.Lock(); applied = append(applied, value); mu.Unlock() },
	})
	eventually(t, func() bool { return calls.Load() >= 3 })
	mu.Lock()
	if len(applied) != 1 || applied[0] != 1 {
		t.Fatalf("applied = %v; failures must not replace the last good value", applied)
	}
	mu.Unlock()
	status := registry.Statuses()[0]
	if status.Mode != ModePoll || status.Stale || status.Error != "venue down" || status.UpdatedAt == 0 {
		t.Fatalf("status = %+v", status)
	}
	// Three missed cadences later the data is stale.
	now.Add(int64(16 * time.Millisecond))
	if !registry.Statuses()[0].Stale || registry.Fresh("funding", "okx:perp:USDT") {
		t.Fatal("data not reported stale after three cadences")
	}
}

func TestStreamReconnectsAndAppliesEveryValue(t *testing.T) {
	registry := NewRegistry()
	var starts atomic.Int32
	var total atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, registry, Spec[int]{
		Dataset: "funding", Market: "hyperliquid:perp:USDC", Interval: time.Minute,
		Stream: func(ctx context.Context, emit func(int)) error {
			if starts.Add(1) == 1 {
				emit(1)
				return errors.New("socket closed")
			}
			emit(2)
			<-ctx.Done()
			return nil
		},
		Apply: func(value int) { total.Add(int32(value)) },
	})
	eventually(t, func() bool { return total.Load() == 3 })
	status := registry.Statuses()[0]
	if status.Mode != ModeStream || status.Stale || status.Error != "" {
		t.Fatalf("status after reconnect = %+v", status)
	}
}

func TestNeverLoadedIsStale(t *testing.T) {
	registry := NewRegistry()
	registry.register("funding", "gate:perp:USDT", ModePoll, time.Second)
	if status := registry.Statuses()[0]; !status.Stale || status.UpdatedAt != 0 {
		t.Fatalf("status = %+v", status)
	}
}
