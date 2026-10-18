// Package resident keeps market-wide venue datasets current in memory: one
// loop per (dataset, market), either polling a whole-market REST read or
// consuming a push stream. A loop keeps the last good value when a read
// fails, and reports itself stale after three missed cadences.
//
// Every venue request a loop makes is marked as background (see
// ratelimit.Background), so resident collection can use at most a quarter of
// a venue's request budget and never delays orders or interactive reads.
// When the budget is not available the read is skipped, not queued.
package resident

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
)

// Mode is how a loop obtains its data.
type Mode string

const (
	ModePoll   Mode = "poll"
	ModeStream Mode = "stream"
)

// staleAfter is how many cadences may pass without an update before a loop's
// data is reported stale.
const staleAfter = 3

// Spec describes one loop. Exactly one of Load and Stream is set.
type Spec[T any] struct {
	Dataset  string
	Market   string
	Interval time.Duration // poll cadence, or the stream's expected cadence
	Load     func(context.Context) (T, error)
	Stream   func(context.Context, func(T)) error
	Apply    func(T)
}

// Status is one loop's health, as shown by /v1/health.
type Status struct {
	Dataset    string `json:"dataset"`
	Market     string `json:"market"`
	Mode       Mode   `json:"mode"`
	IntervalMs int64  `json:"intervalMs"`
	UpdatedAt  int64  `json:"updatedAt,omitempty"`
	Stale      bool   `json:"stale"`
	Error      string `json:"error,omitempty"`
}

// Registry tracks every loop's status.
type Registry struct {
	mu    sync.Mutex
	loops map[string]*state
	now   func() time.Time
}

type state struct {
	dataset, market string
	mode            Mode
	interval        time.Duration
	updatedAt       time.Time
	err             string
}

func NewRegistry() *Registry {
	return &Registry{loops: map[string]*state{}, now: time.Now}
}

// Statuses lists every loop, ordered by dataset then market.
func (registry *Registry) Statuses() []Status {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	now := registry.now()
	result := make([]Status, 0, len(registry.loops))
	for _, loop := range registry.loops {
		status := Status{
			Dataset: loop.dataset, Market: loop.market, Mode: loop.mode,
			IntervalMs: loop.interval.Milliseconds(), Stale: loop.stale(now), Error: loop.err,
		}
		if !loop.updatedAt.IsZero() {
			status.UpdatedAt = loop.updatedAt.UnixMilli()
		}
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Dataset != result[j].Dataset {
			return result[i].Dataset < result[j].Dataset
		}
		return result[i].Market < result[j].Market
	})
	return result
}

// Fresh reports whether a loop has delivered data within its stale window.
func (registry *Registry) Fresh(dataset, market string) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	loop := registry.loops[dataset+"|"+market]
	return loop != nil && !loop.stale(registry.now())
}

func (loop *state) stale(now time.Time) bool {
	return loop.updatedAt.IsZero() || now.Sub(loop.updatedAt) > staleAfter*loop.interval
}

// Declare lists a loop before it starts, so status reports every configured
// (dataset, market) from startup — as stale until its first success.
func (registry *Registry) Declare(dataset, market string, mode Mode, interval time.Duration) {
	registry.register(dataset, market, mode, interval)
}

func (registry *Registry) register(dataset, market string, mode Mode, interval time.Duration) *state {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	key := dataset + "|" + market
	if existing := registry.loops[key]; existing != nil {
		existing.mode, existing.interval = mode, interval
		return existing
	}
	loop := &state{dataset: dataset, market: market, mode: mode, interval: interval}
	registry.loops[key] = loop
	return loop
}

func (registry *Registry) succeeded(loop *state) {
	registry.mu.Lock()
	recovered := loop.err != ""
	loop.updatedAt, loop.err = registry.now(), ""
	registry.mu.Unlock()
	if recovered {
		slog.Info("resident dataset recovered", "dataset", loop.dataset, "market", loop.market)
	}
}

func (registry *Registry) failed(loop *state, err error) {
	message := "stream ended"
	if err != nil {
		message = err.Error()
	}
	if len(message) > 200 {
		message = message[:200]
	}
	registry.mu.Lock()
	first := loop.err == ""
	loop.err = message
	registry.mu.Unlock()
	if first {
		slog.Warn("resident dataset read failed", "dataset", loop.dataset, "market", loop.market, "error", message)
	}
}

// Run keeps one dataset of one market current until ctx ends.
func Run[T any](ctx context.Context, registry *Registry, spec Spec[T]) {
	ctx = ratelimit.Background(ctx)
	if spec.Stream != nil {
		runStream(ctx, registry, spec)
		return
	}
	loop := registry.register(spec.Dataset, spec.Market, ModePoll, spec.Interval)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		callCtx, cancel := context.WithTimeout(ctx, max(spec.Interval, 20*time.Second))
		value, err := spec.Load(callCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			registry.failed(loop, err)
		} else {
			spec.Apply(value)
			registry.succeeded(loop)
		}
		timer.Reset(spec.Interval)
	}
}

func runStream[T any](ctx context.Context, registry *Registry, spec Spec[T]) {
	loop := registry.register(spec.Dataset, spec.Market, ModeStream, spec.Interval)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := spec.Stream(ctx, func(value T) {
			spec.Apply(value)
			registry.succeeded(loop)
		})
		if ctx.Err() != nil {
			return
		}
		registry.failed(loop, err)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		jitter := time.Duration(rand.Int64N(int64(backoff/4) + 1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
