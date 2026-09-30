package authorization

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type schedulerRunner struct {
	mu       sync.Mutex
	calls    int
	active   int
	maxAlive int
	errors   []error
	started  chan int
	release  chan struct{}
	result   AuthorizationReconciliationResult
}

func (r *schedulerRunner) RunOnce(ctx context.Context) (AuthorizationReconciliationResult, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.active++
	if r.active > r.maxAlive {
		r.maxAlive = r.active
	}
	var err error
	if call <= len(r.errors) {
		err = r.errors[call-1]
	}
	r.mu.Unlock()
	if r.started != nil {
		r.started <- call
	}
	if r.release != nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-r.release:
		}
	}
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return r.result, err
}

func (r *schedulerRunner) snapshot() (calls, maxAlive int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.maxAlive
}

type schedulerTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newSchedulerForTest(t *testing.T, runner ReconciliationCycleRunner, interval time.Duration, logger *slog.Logger) (*AuthorizationReconciliationScheduler, *schedulerTicker) {
	t.Helper()
	scheduler, err := NewAuthorizationReconciliationScheduler(runner, AuthorizationReconciliationSchedulerConfig{Interval: interval}, logger)
	if err != nil {
		t.Fatal(err)
	}
	ticker := &schedulerTicker{ticks: make(chan time.Time, 8), stopped: make(chan struct{})}
	scheduler.newTicker = func(got time.Duration) (<-chan time.Time, func()) {
		if got != interval {
			t.Errorf("ticker interval = %v, want %v", got, interval)
		}
		return ticker.ticks, func() { ticker.once.Do(func() { close(ticker.stopped) }) }
	}
	return scheduler, ticker
}

func awaitSchedulerCall(t *testing.T, started <-chan int, want int) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("cycle = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("cycle %d did not start", want)
	}
}

func awaitSchedulerExit(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("Run error = %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestAuthorizationReconciliationSchedulerImmediateTicksAndNoOverlap(t *testing.T) {
	runner := &schedulerRunner{
		started: make(chan int, 4), release: make(chan struct{}, 4),
		result: AuthorizationReconciliationResult{Managed: 7},
	}
	config := AuthorizationReconciliationSchedulerConfig{Interval: time.Minute}
	scheduler, ticker := newSchedulerForTest(t, runner, config.Interval, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()

	awaitSchedulerCall(t, runner.started, 1)
	ticker.ticks <- time.Now()
	select {
	case call := <-runner.started:
		t.Fatalf("overlapping cycle started: %d", call)
	default:
	}
	runner.release <- struct{}{}
	awaitSchedulerCall(t, runner.started, 2)
	ticker.ticks <- time.Now()
	runner.release <- struct{}{}
	awaitSchedulerCall(t, runner.started, 3)
	runner.release <- struct{}{}
	cancel()
	awaitSchedulerExit(t, done, context.Canceled)
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("ticker was not stopped")
	}
	if calls, maxAlive := runner.snapshot(); calls != 3 || maxAlive != 1 {
		t.Fatalf("calls/max concurrent = %d/%d, want 3/1", calls, maxAlive)
	}
	if config.Interval != time.Minute || runner.result.Managed != 7 {
		t.Fatalf("scheduler mutated config/result: %+v/%+v", config, runner.result)
	}
}

func TestAuthorizationReconciliationSchedulerContinuesAfterCycleError(t *testing.T) {
	runner := &schedulerRunner{started: make(chan int, 2), errors: []error{errors.New("Keycloak unavailable"), nil}}
	var logs bytes.Buffer
	scheduler, ticker := newSchedulerForTest(t, runner, time.Minute, slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()

	awaitSchedulerCall(t, runner.started, 1)
	ticker.ticks <- time.Now()
	awaitSchedulerCall(t, runner.started, 2)
	cancel()
	awaitSchedulerExit(t, done, context.Canceled)
	if strings.Count(logs.String(), `"msg":"reconciliation_cycle_failed"`) != 1 || strings.Contains(logs.String(), "Keycloak unavailable") {
		t.Fatalf("unexpected failure log: %s", logs.String())
	}
}

func TestAuthorizationReconciliationSchedulerCancellationBoundaries(t *testing.T) {
	t.Run("before run", func(t *testing.T) {
		runner := &schedulerRunner{}
		scheduler, _ := newSchedulerForTest(t, runner, time.Minute, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := scheduler.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
		if calls, _ := runner.snapshot(); calls != 0 {
			t.Fatalf("cycles = %d, want 0", calls)
		}
	})

	t.Run("while waiting", func(t *testing.T) {
		runner := &schedulerRunner{started: make(chan int, 1)}
		scheduler, ticker := newSchedulerForTest(t, runner, time.Minute, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- scheduler.Run(ctx) }()
		awaitSchedulerCall(t, runner.started, 1)
		cancel()
		awaitSchedulerExit(t, done, context.Canceled)
		select {
		case <-ticker.stopped:
		default:
			t.Fatal("ticker was not stopped")
		}
	})

	t.Run("during cycle", func(t *testing.T) {
		runner := &schedulerRunner{started: make(chan int, 1), release: make(chan struct{})}
		scheduler, _ := newSchedulerForTest(t, runner, time.Minute, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- scheduler.Run(ctx) }()
		awaitSchedulerCall(t, runner.started, 1)
		cancel()
		awaitSchedulerExit(t, done, context.Canceled)
		if calls, _ := runner.snapshot(); calls != 1 {
			t.Fatalf("cycles = %d, want 1", calls)
		}
	})
}

func TestNewAuthorizationReconciliationSchedulerRejectsInvalidConfig(t *testing.T) {
	if _, err := NewAuthorizationReconciliationScheduler(nil, AuthorizationReconciliationSchedulerConfig{Interval: time.Second}, nil); err == nil {
		t.Fatal("nil runner accepted")
	}
	runner := &schedulerRunner{}
	for _, interval := range []time.Duration{0, -time.Second} {
		if _, err := NewAuthorizationReconciliationScheduler(runner, AuthorizationReconciliationSchedulerConfig{Interval: interval}, nil); err == nil {
			t.Fatalf("interval %v accepted", interval)
		}
	}
	scheduler, err := NewAuthorizationReconciliationScheduler(runner, AuthorizationReconciliationSchedulerConfig{Interval: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Run(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}
