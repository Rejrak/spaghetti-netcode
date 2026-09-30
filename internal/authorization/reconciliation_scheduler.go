package authorization

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"spaghetti/internal/observability"
)

type ReconciliationCycleRunner interface {
	RunOnce(context.Context) (AuthorizationReconciliationResult, error)
}

type AuthorizationReconciliationSchedulerConfig struct {
	Interval time.Duration
}

type AuthorizationReconciliationScheduler struct {
	runner    ReconciliationCycleRunner
	interval  time.Duration
	logger    *slog.Logger
	newTicker func(time.Duration) (<-chan time.Time, func())
}

func NewAuthorizationReconciliationScheduler(
	runner ReconciliationCycleRunner,
	config AuthorizationReconciliationSchedulerConfig,
	logger *slog.Logger,
) (*AuthorizationReconciliationScheduler, error) {
	if isNilDependency(runner) {
		return nil, fmt.Errorf("nil reconciliation cycle runner")
	}
	if config.Interval <= 0 {
		return nil, fmt.Errorf("reconciliation interval must be positive")
	}
	return &AuthorizationReconciliationScheduler{
		runner:   runner,
		interval: config.Interval,
		logger:   logger,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}, nil
}

// Run always reconciles immediately after process start. Restart recovery is
// provided by the persistent managed-subject inventory and authoritative Alpha
// state; the scheduler intentionally persists no execution or batch state.
func (s *AuthorizationReconciliationScheduler) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("nil reconciliation scheduler context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.runCycle(ctx); err != nil {
		return err
	}

	ticks, stop := s.newTicker(s.interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticks:
			if err := s.runCycle(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *AuthorizationReconciliationScheduler) runCycle(ctx context.Context) error {
	started := time.Now()
	_, err := s.runner.RunOnce(ctx)
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.ErrorContext(ctx, observability.EventReconciliationCycleFailed,
		"duration_ms", time.Since(started).Milliseconds(),
		"error_category", "cycle_error",
	)
	return nil
}
