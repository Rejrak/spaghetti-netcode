package authorization

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"spaghetti/internal/observability"
)

type ManagedSubjectStore interface {
	EnsureManagedSubject(context.Context, string) error
	ListManagedSubjects(context.Context) ([]string, error)
}

type SubjectDiscoverer interface {
	ListSubjects(context.Context) ([]string, error)
}

type authorizationSubjectSource interface {
	FetchAuthorizationSubjectCandidates(context.Context) ([]string, error)
}

type KeycloakSubjectDiscoverer struct {
	source authorizationSubjectSource
}

func NewKeycloakSubjectDiscoverer(source authorizationSubjectSource) (*KeycloakSubjectDiscoverer, error) {
	if isNilDependency(source) {
		return nil, fmt.Errorf("nil Keycloak user source")
	}
	return &KeycloakSubjectDiscoverer{source: source}, nil
}

func (d *KeycloakSubjectDiscoverer) ListSubjects(ctx context.Context) ([]string, error) {
	if ctx == nil {
		return nil, fmt.Errorf("nil subject discovery context")
	}
	subjects, err := d.source.FetchAuthorizationSubjectCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch Keycloak users: %w", err)
	}
	return canonicalSubjects(subjects)
}

type revocationReconciler interface {
	Reconcile(context.Context, AuthorizationReconcileRequest) (AuthorizationReconcileResult, error)
}

type AuthorizationReconciliationWorkerConfig struct {
	ChainID    string
	PolicyHash []byte
}

type AuthorizationReconciliationResult struct {
	Discovered         int
	Managed            int
	NoopPolicyAllows   int
	NoopNoCurrent      int
	NoopAlreadyRevoked int
	Revoked            int
	Failed             int
}

type AuthorizationReconciliationWorker struct {
	discoverer SubjectDiscoverer
	store      ManagedSubjectStore
	reconciler revocationReconciler
	config     AuthorizationReconciliationWorkerConfig
	logger     *slog.Logger
}

func NewAuthorizationReconciliationWorker(
	discoverer SubjectDiscoverer,
	store ManagedSubjectStore,
	reconciler revocationReconciler,
	config AuthorizationReconciliationWorkerConfig,
	logger *slog.Logger,
) (*AuthorizationReconciliationWorker, error) {
	if isNilDependency(discoverer) {
		return nil, fmt.Errorf("nil subject discoverer")
	}
	if isNilDependency(store) {
		return nil, fmt.Errorf("nil managed subject store")
	}
	if isNilDependency(reconciler) {
		return nil, fmt.Errorf("nil revocation reconciler")
	}
	if strings.TrimSpace(config.ChainID) == "" {
		return nil, fmt.Errorf("empty reconciliation chain id")
	}
	if len(config.PolicyHash) != sha256.Size {
		return nil, fmt.Errorf("reconciliation policy hash must be 32 bytes")
	}
	config.PolicyHash = append([]byte(nil), config.PolicyHash...)
	return &AuthorizationReconciliationWorker{
		discoverer: discoverer,
		store:      store,
		reconciler: reconciler,
		config:     config,
		logger:     logger,
	}, nil
}

func (w *AuthorizationReconciliationWorker) RunOnce(ctx context.Context) (AuthorizationReconciliationResult, error) {
	if ctx == nil {
		return AuthorizationReconciliationResult{}, fmt.Errorf("nil reconciliation context")
	}
	if err := ctx.Err(); err != nil {
		return AuthorizationReconciliationResult{}, err
	}
	started := time.Now()
	discovered, err := w.discoverer.ListSubjects(ctx)
	if err != nil {
		return AuthorizationReconciliationResult{}, fmt.Errorf("discover managed subjects: %w", err)
	}
	discovered, err = canonicalSubjects(discovered)
	if err != nil {
		return AuthorizationReconciliationResult{}, fmt.Errorf("discover managed subjects: %w", err)
	}
	result := AuthorizationReconciliationResult{Discovered: len(discovered)}
	for _, subject := range discovered {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := w.store.EnsureManagedSubject(ctx, subject); err != nil {
			return result, fmt.Errorf("persist managed subject %q: %w", subject, err)
		}
	}
	managed, err := w.store.ListManagedSubjects(ctx)
	if err != nil {
		return result, fmt.Errorf("list managed subjects: %w", err)
	}
	managed, err = canonicalSubjects(managed)
	if err != nil {
		return result, fmt.Errorf("list managed subjects: %w", err)
	}
	result.Managed = len(managed)

	var failures []error
	for _, subject := range managed {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		reconciled, err := w.reconciler.Reconcile(ctx, AuthorizationReconcileRequest{
			Subject: subject, MsgTypeURL: MsgSendTypeURL, ChainID: w.config.ChainID,
			PolicyHash: append([]byte(nil), w.config.PolicyHash...),
		})
		if err != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("reconcile managed subject %q: %w", subject, err))
			if ctx.Err() != nil {
				return result, errors.Join(failures...)
			}
			continue
		}
		switch reconciled.Status {
		case ReconcileNoopPolicyAllows:
			result.NoopPolicyAllows++
		case ReconcileNoopNoCurrent:
			result.NoopNoCurrent++
		case ReconcileNoopAlreadyRevoked:
			result.NoopAlreadyRevoked++
		case ReconcileRevoked:
			result.Revoked++
		default:
			result.Failed++
			failures = append(failures, fmt.Errorf("reconcile managed subject %q returned unknown status %q", subject, reconciled.Status))
		}
	}
	w.logCycle(ctx, result, time.Since(started))
	return result, errors.Join(failures...)
}

func canonicalSubjects(subjects []string) ([]string, error) {
	unique := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		if err := ValidateAccountAddress(subject); err != nil {
			return nil, fmt.Errorf("invalid managed subject %q: %w", subject, err)
		}
		unique[subject] = struct{}{}
	}
	canonical := make([]string, 0, len(unique))
	for subject := range unique {
		canonical = append(canonical, subject)
	}
	sort.Strings(canonical)
	return canonical, nil
}

func (w *AuthorizationReconciliationWorker) logCycle(ctx context.Context, result AuthorizationReconciliationResult, duration time.Duration) {
	logger := w.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, observability.EventReconciliationCycle,
		"discovered", result.Discovered,
		"managed", result.Managed,
		"noop_policy_allows", result.NoopPolicyAllows,
		"noop_no_current", result.NoopNoCurrent,
		"noop_already_revoked", result.NoopAlreadyRevoked,
		"revoked", result.Revoked,
		"failed", result.Failed,
		"duration_ms", duration.Milliseconds(),
	)
}
