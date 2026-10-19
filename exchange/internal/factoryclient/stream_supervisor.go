package factoryclient

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// Reconnect backoff for supervised venue streams; overridable in tests.
var supervisedStreamDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

// errStreamResync asks the supervisor to reconnect at once: the venue stream
// lost continuity (a sequence gap) and a fresh snapshot is needed.
var errStreamResync = errors.New("stream lost continuity")

// streamSession is one connection attempt. It blocks until the connection
// ends, calling ready once it is subscribed and live.
type streamSession func(ctx context.Context, ready func()) error

// startSupervisedStream keeps one venue connection alive until stopped or
// lifecycle ends, reconnecting with backoff. It returns after the first
// connection is live, or with the first attempt's error so a caller learns
// at once that a stream cannot start (bad key, unknown instrument).
//
// status receives Active on every (re)connect, with a new generation, and
// Connecting while the stream is down. fatal, when set, classifies an error
// that retrying will not heal; it is reported through onError and the
// supervisor keeps retrying slowly.
func startSupervisedStream(
	startCtx, lifecycle context.Context,
	session streamSession,
	status func(backendStatus),
	onError func(error),
	fatal func(error) bool,
) (func() error, error) {
	if startCtx == nil {
		startCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(lifecycle)
	done := make(chan struct{})
	first := make(chan error, 1)
	var firstOnce sync.Once
	report := func(err error) { firstOnce.Do(func() { first <- err }) }
	go func() {
		defer close(done)
		attempt := 0
		var generation uint64
		for ctx.Err() == nil {
			generation++
			current, ready := generation, false
			err := session(ctx, func() {
				ready, attempt = true, 0
				report(nil)
				if status != nil {
					status(backendStatus{State: exchange.SubscriptionActive, Generation: current, Time: time.Now().UTC()})
				}
			})
			if ctx.Err() != nil {
				report(ctx.Err())
				return
			}
			if !ready {
				if err == nil {
					err = errors.New("stream ended before it was live")
				}
				report(err)
			}
			if err != nil && fatal != nil && fatal(err) && onError != nil {
				onError(err)
			}
			reason := "stream disconnected"
			if errors.Is(err, errStreamResync) {
				reason = "stream lost continuity; resubscribing"
			}
			if status != nil {
				status(backendStatus{State: exchange.SubscriptionConnecting, Generation: current, Reason: reason, Time: time.Now().UTC()})
			}
			delay := time.Duration(0)
			if !errors.Is(err, errStreamResync) || attempt > 0 {
				delay = supervisedStreamDelays[min(attempt, len(supervisedStreamDelays)-1)]
			}
			attempt++
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
	select {
	case err := <-first:
		if err != nil {
			cancel()
			<-done
			return nil, err
		}
	case <-startCtx.Done():
		cancel()
		<-done
		return nil, startCtx.Err()
	}
	var once sync.Once
	return func() error {
		once.Do(func() {
			cancel()
			<-done
		})
		return nil
	}, nil
}

// startPolling runs poll every interval until stopped. The first poll runs
// before it returns and its error is returned, so a stream that cannot be
// served fails at once.
func startPolling(startCtx, lifecycle context.Context, interval time.Duration, poll func(context.Context) error, status func(backendStatus)) (func() error, error) {
	if err := poll(startCtx); err != nil {
		return nil, err
	}
	if status != nil {
		status(backendStatus{State: exchange.SubscriptionActive, Generation: 1, Time: time.Now().UTC()})
	}
	ctx, cancel := context.WithCancel(lifecycle)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		failing := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			callCtx, cancelCall := context.WithTimeout(ctx, interval+10*time.Second)
			err := poll(callCtx)
			cancelCall()
			if ctx.Err() != nil {
				return
			}
			switch {
			case err != nil && !failing:
				failing = true
				if status != nil {
					status(backendStatus{State: exchange.SubscriptionConnecting, Reason: "poll failed", Time: time.Now().UTC()})
				}
			case err == nil && failing:
				failing = false
				if status != nil {
					status(backendStatus{State: exchange.SubscriptionActive, Time: time.Now().UTC()})
				}
			}
		}
	}()
	var once sync.Once
	return func() error {
		once.Do(func() {
			cancel()
			<-done
		})
		return nil
	}, nil
}
