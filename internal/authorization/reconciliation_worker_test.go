package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const (
	workerSubjectC = "cosmos1wmrv55gqds8zlfzk33rr7gp4cav5v97v84qwp0"
	workerSubjectD = "cosmos1hu2zq2fsp8245r7ud9upk6r7pxuva3qwyg4n0h"
)

type workerUserSource struct {
	subjects []string
	err      error
	calls    int
}

func (s *workerUserSource) FetchAuthorizationSubjectCandidates(context.Context) ([]string, error) {
	s.calls++
	return append([]string(nil), s.subjects...), s.err
}

func TestKeycloakSubjectDiscoverer(t *testing.T) {
	source := &workerUserSource{subjects: []string{testSubject, testReceiver, testSubject}}
	discoverer, err := NewKeycloakSubjectDiscoverer(source)
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := discoverer.ListSubjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{testReceiver, testSubject}; !reflect.DeepEqual(subjects, want) {
		t.Fatalf("subjects = %v, want %v", subjects, want)
	}

	source.subjects = []string{"cosmos-malformed"}
	if _, err := discoverer.ListSubjects(context.Background()); err == nil {
		t.Fatal("malformed Keycloak address accepted")
	}
	source.err = errors.New("Keycloak unavailable")
	if _, err := discoverer.ListSubjects(context.Background()); err == nil {
		t.Fatal("Keycloak failure accepted")
	}
}

type workerDiscoverer struct {
	subjects []string
	err      error
	calls    int
}

func (d *workerDiscoverer) ListSubjects(context.Context) ([]string, error) {
	d.calls++
	return append([]string(nil), d.subjects...), d.err
}

type workerStore struct {
	subjects  map[string]struct{}
	ensured   []string
	ensureErr error
	listErr   error
	listCalls int
}

func (s *workerStore) EnsureManagedSubject(_ context.Context, subject string) error {
	s.ensured = append(s.ensured, subject)
	if s.ensureErr != nil {
		return s.ensureErr
	}
	if s.subjects == nil {
		s.subjects = make(map[string]struct{})
	}
	s.subjects[subject] = struct{}{}
	return nil
}

func (s *workerStore) ListManagedSubjects(context.Context) ([]string, error) {
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	result := make([]string, 0, len(s.subjects))
	for subject := range s.subjects {
		result = append(result, subject)
	}
	return result, nil
}

type workerReconciler struct {
	results  map[string]AuthorizationReconcileResult
	errors   map[string]error
	calls    []string
	requests []AuthorizationReconcileRequest
	after    func(string)
}

func (r *workerReconciler) Reconcile(_ context.Context, request AuthorizationReconcileRequest) (AuthorizationReconcileResult, error) {
	r.calls = append(r.calls, request.Subject)
	request.PolicyHash = append([]byte(nil), request.PolicyHash...)
	r.requests = append(r.requests, request)
	if r.after != nil {
		r.after(request.Subject)
	}
	return r.results[request.Subject], r.errors[request.Subject]
}

func newWorker(t *testing.T, discoverer SubjectDiscoverer, store ManagedSubjectStore, reconciler revocationReconciler, hash []byte, logger *slog.Logger) *AuthorizationReconciliationWorker {
	t.Helper()
	worker, err := NewAuthorizationReconciliationWorker(discoverer, store, reconciler, AuthorizationReconciliationWorkerConfig{
		ChainID: "alpha-1", PolicyHash: hash,
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestAuthorizationReconciliationWorkerDeletionSafetyAndOrdering(t *testing.T) {
	discoverer := &workerDiscoverer{subjects: []string{testSubject, testReceiver, testSubject}}
	store := &workerStore{}
	reconciler := &workerReconciler{results: map[string]AuthorizationReconcileResult{
		testSubject:  {Status: ReconcileNoopPolicyAllows},
		testReceiver: {Status: ReconcileNoopNoCurrent},
	}}
	worker := newWorker(t, discoverer, store, reconciler, bytes.Repeat([]byte{1}, sha256.Size), nil)

	first, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Discovered != 2 || first.Managed != 2 || first.NoopPolicyAllows != 1 || first.NoopNoCurrent != 1 {
		t.Fatalf("first result = %+v", first)
	}
	wantOrder := []string{testReceiver, testSubject}
	if !reflect.DeepEqual(store.ensured, wantOrder) || !reflect.DeepEqual(reconciler.calls, wantOrder) {
		t.Fatalf("ensure/reconcile order = %v/%v, want %v", store.ensured, reconciler.calls, wantOrder)
	}

	discoverer.subjects = nil
	reconciler.calls = nil
	second, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Discovered != 0 || second.Managed != 2 || !reflect.DeepEqual(reconciler.calls, wantOrder) {
		t.Fatalf("deleted-subject run = %+v calls=%v", second, reconciler.calls)
	}
}

func TestAuthorizationReconciliationWorkerCountsAndContinuesFailures(t *testing.T) {
	subjects := []string{testSubject, testReceiver, workerSubjectC, workerSubjectD}
	discoverer := &workerDiscoverer{subjects: subjects}
	store := &workerStore{}
	reconciler := &workerReconciler{
		results: map[string]AuthorizationReconcileResult{
			testSubject:    {Status: ReconcileNoopPolicyAllows},
			testReceiver:   {Status: ReconcileNoopNoCurrent},
			workerSubjectC: {Status: ReconcileNoopAlreadyRevoked},
			workerSubjectD: {Status: ReconcileRevoked},
		},
		errors: map[string]error{testReceiver: errors.New("reader unavailable")},
	}
	var logs bytes.Buffer
	worker := newWorker(t, discoverer, store, reconciler, bytes.Repeat([]byte{2}, sha256.Size), slog.New(slog.NewJSONHandler(&logs, nil)))
	result, err := worker.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), testReceiver) {
		t.Fatalf("aggregate error = %v", err)
	}
	if result.Discovered != 4 || result.Managed != 4 || result.NoopPolicyAllows != 1 ||
		result.NoopAlreadyRevoked != 1 || result.Revoked != 1 || result.Failed != 1 || result.NoopNoCurrent != 0 {
		t.Fatalf("result = %+v", result)
	}
	wantOrder := []string{testReceiver, testSubject, workerSubjectD, workerSubjectC}
	if !reflect.DeepEqual(reconciler.calls, wantOrder) {
		t.Fatalf("reconciliation order/calls = %v, want %v", reconciler.calls, wantOrder)
	}
	if strings.Count(logs.String(), `"msg":"reconciliation_cycle"`) != 1 || strings.Contains(logs.String(), testSubject) {
		t.Fatalf("unexpected summary log: %s", logs.String())
	}
}

func TestAuthorizationReconciliationWorkerFatalBoundaries(t *testing.T) {
	t.Run("discovery", func(t *testing.T) {
		discoverer := &workerDiscoverer{err: errors.New("discovery failed")}
		store := &workerStore{}
		reconciler := &workerReconciler{}
		worker := newWorker(t, discoverer, store, reconciler, bytes.Repeat([]byte{3}, sha256.Size), nil)
		if _, err := worker.RunOnce(context.Background()); err == nil || len(store.ensured) != 0 || len(reconciler.calls) != 0 {
			t.Fatalf("error/ensure/reconcile = %v/%v/%v", err, store.ensured, reconciler.calls)
		}
	})
	t.Run("persistence", func(t *testing.T) {
		discoverer := &workerDiscoverer{subjects: []string{testSubject}}
		store := &workerStore{ensureErr: errors.New("write failed")}
		reconciler := &workerReconciler{}
		worker := newWorker(t, discoverer, store, reconciler, bytes.Repeat([]byte{3}, sha256.Size), nil)
		if _, err := worker.RunOnce(context.Background()); err == nil || len(reconciler.calls) != 0 {
			t.Fatalf("error/reconcile = %v/%v", err, reconciler.calls)
		}
	})
	t.Run("listing", func(t *testing.T) {
		discoverer := &workerDiscoverer{}
		store := &workerStore{listErr: errors.New("read failed")}
		reconciler := &workerReconciler{}
		worker := newWorker(t, discoverer, store, reconciler, bytes.Repeat([]byte{3}, sha256.Size), nil)
		if _, err := worker.RunOnce(context.Background()); err == nil || len(reconciler.calls) != 0 {
			t.Fatalf("error/reconcile = %v/%v", err, reconciler.calls)
		}
	})
}

func TestAuthorizationReconciliationWorkerCancellationAndConfigCopy(t *testing.T) {
	hash := bytes.Repeat([]byte{4}, sha256.Size)
	original := append([]byte(nil), hash...)
	discoverer := &workerDiscoverer{subjects: []string{testSubject, testReceiver}}
	store := &workerStore{}
	ctx, cancel := context.WithCancel(context.Background())
	reconciler := &workerReconciler{results: map[string]AuthorizationReconcileResult{
		testSubject: {Status: ReconcileNoopPolicyAllows},
	}, after: func(string) { cancel() }}
	worker := newWorker(t, discoverer, store, reconciler, hash, nil)
	hash[0] = 99
	result, err := worker.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) || len(reconciler.calls) != 1 {
		t.Fatalf("cancellation result/error/calls = %+v/%v/%v", result, err, reconciler.calls)
	}
	if !reflect.DeepEqual(reconciler.requests[0].PolicyHash, original) || reconciler.requests[0].ChainID != "alpha-1" || reconciler.requests[0].MsgTypeURL != MsgSendTypeURL {
		t.Fatalf("trusted request mutated or incomplete: %+v", reconciler.requests[0])
	}
}
