package authorization

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"spaghetti/internal/remote/policy"
	"spaghetti/internal/user"
)

type reconcileStateReader struct {
	record               AuthorizationRecord
	recordFound          bool
	recordErr            error
	issuerSetID          uint64
	issuerSetFound       bool
	issuerSetErr         error
	lastBatchID          uint64
	lastBatchFound       bool
	lastBatchErr         error
	authorizationCalls   int
	issuerSetCalls       int
	lastBatchCalls       int
	authorizationSubject string
	authorizationMsgType string
	issuerSetPolicyID    string
	issuerSetMsgType     string
	lastBatchIssuerSetID uint64
}

func (r *reconcileStateReader) Authorization(_ context.Context, subject, msgTypeURL string) (AuthorizationRecord, bool, error) {
	r.authorizationCalls++
	r.authorizationSubject = subject
	r.authorizationMsgType = msgTypeURL
	return cloneAuthorizationRecord(r.record), r.recordFound, r.recordErr
}

func (r *reconcileStateReader) CurrentIssuerSet(_ context.Context, policyID, msgTypeURL string) (uint64, bool, error) {
	r.issuerSetCalls++
	r.issuerSetPolicyID = policyID
	r.issuerSetMsgType = msgTypeURL
	return r.issuerSetID, r.issuerSetFound, r.issuerSetErr
}

func (r *reconcileStateReader) LastAppliedBatchID(_ context.Context, issuerSetID uint64) (uint64, bool, error) {
	r.lastBatchCalls++
	r.lastBatchIssuerSetID = issuerSetID
	return r.lastBatchID, r.lastBatchFound, r.lastBatchErr
}

type reconcileFixture struct {
	source    *issuerAttributeSource
	evaluator *issuerPolicyEvaluator
	state     *reconcileStateReader
	signers   []*issuerBatchSigner
	publisher *issuerBatchPublisher
	confirmer *issuerBatchConfirmer
	service   *AuthorizationRevocationReconciler
	request   AuthorizationReconcileRequest
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	current := AuthorizationRecord{
		AuthorizationID: "auth-current", Subject: testSubject, MsgTypeURL: MsgSendTypeURL,
		PolicyID: "policy-bank-send", PolicyVersion: 7, IssuerSetID: 8,
		ValidFromHeight: 10, ValidUntilHeight: 100,
		BankSendConstraints: BankSendConstraints{Denom: "uatom", Receiver: testReceiver, MaxAmount: "5000"},
	}
	f := &reconcileFixture{
		source: &issuerAttributeSource{attributes: &user.Attributes{Perms: map[string]bool{}}},
		evaluator: &issuerPolicyEvaluator{decision: policy.PolicyDecision{
			Allow: false, ReasonCode: policy.ReasonPolicyMismatch, Reason: "permission removed",
			PolicyID: "policy-bank-send", PolicyVersion: "7",
		}},
		state: &reconcileStateReader{
			record: current, recordFound: true, issuerSetID: 9, issuerSetFound: true,
			lastBatchID: 41, lastBatchFound: true,
		},
		signers:   []*issuerBatchSigner{{issuerID: "issuer-beta"}, {issuerID: "issuer-alpha"}},
		publisher: &issuerBatchPublisher{result: BroadcastResult{TxHash: "REVOKE-TX"}},
		confirmer: &issuerBatchConfirmer{result: CommitResult{TxHash: "REVOKE-TX", Height: 99}},
		request: AuthorizationReconcileRequest{
			Subject: testSubject, MsgTypeURL: MsgSendTypeURL, ChainID: "alpha-1",
			PolicyHash: bytes.Repeat([]byte{0x44}, 32),
		},
	}
	service, err := NewAuthorizationRevocationReconciler(
		f.source, f.evaluator, f.state,
		[]BatchSigner{f.signers[0], f.signers[1]}, f.publisher, f.confirmer, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	return f
}

func TestAuthorizationRevocationReconcilerNoops(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*reconcileFixture)
		wantStatus AuthorizationReconcileStatus
		wantReads  int
	}{
		{name: "policy allows", mutate: func(f *reconcileFixture) { f.evaluator.decision.Allow = true }, wantStatus: ReconcileNoopPolicyAllows},
		{name: "no current authorization", mutate: func(f *reconcileFixture) { f.state.recordFound = false }, wantStatus: ReconcileNoopNoCurrent, wantReads: 1},
		{name: "already revoked", mutate: func(f *reconcileFixture) { f.state.record.Revoked = true }, wantStatus: ReconcileNoopAlreadyRevoked, wantReads: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReconcileFixture(t)
			tt.mutate(f)
			result, err := f.service.Reconcile(context.Background(), f.request)
			if err != nil || result.Status != tt.wantStatus {
				t.Fatalf("Reconcile() = %+v, %v", result, err)
			}
			if f.state.authorizationCalls != tt.wantReads || f.publisher.calls != 0 || f.confirmer.calls != 0 {
				t.Fatalf("calls read/publish/confirm = %d/%d/%d", f.state.authorizationCalls, f.publisher.calls, f.confirmer.calls)
			}
		})
	}
}

func TestAuthorizationRevocationReconcilerRevokesWithCurrentIssuerSet(t *testing.T) {
	f := newReconcileFixture(t)
	requestBefore := f.request
	requestBefore.PolicyHash = append([]byte(nil), f.request.PolicyHash...)
	currentBefore := cloneAuthorizationRecord(f.state.record)

	result, err := f.service.Reconcile(context.Background(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ReconcileRevoked || result.AuthorizationID != "auth-current" || result.BatchID != 42 || result.TxHash != "REVOKE-TX" || result.Height != 99 {
		t.Fatalf("result = %+v", result)
	}
	if f.publisher.calls != 1 || f.confirmer.calls != 1 || f.signers[0].calls != 1 || f.signers[1].calls != 1 {
		t.Fatalf("pipeline calls publish/confirm/signers = %d/%d/%d/%d", f.publisher.calls, f.confirmer.calls, f.signers[0].calls, f.signers[1].calls)
	}
	if f.source.address != testSubject || f.evaluator.input.Subject != testSubject || f.evaluator.input.Operation != MsgSendTypeURL {
		t.Fatalf("source/policy inputs = %q/%+v", f.source.address, f.evaluator.input)
	}
	if f.state.authorizationSubject != testSubject || f.state.authorizationMsgType != MsgSendTypeURL ||
		f.state.issuerSetPolicyID != "policy-bank-send" || f.state.issuerSetMsgType != MsgSendTypeURL || f.state.lastBatchIssuerSetID != 9 {
		t.Fatalf("state query inputs = %+v", f.state)
	}
	if !reflect.DeepEqual(f.publisher.batch, f.confirmer.batch) || f.confirmer.broadcast != f.publisher.result {
		t.Fatal("publisher/confirmer batch mismatch")
	}
	record := f.publisher.batch.SignDoc.Records[0]
	want := cloneAuthorizationRecord(currentBefore)
	want.Revoked = true
	want.IssuerSetID = 9
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("revoked record = %+v, want %+v", record, want)
	}
	if f.publisher.batch.SignDoc.BatchID != 42 || f.publisher.batch.SignDoc.IssuerSetID != 9 {
		t.Fatalf("sign doc = %+v", f.publisher.batch.SignDoc)
	}
	_, wantHash, err := CanonicalBatchSignBytes(f.publisher.batch.SignDoc)
	if err != nil || result.BatchHash != wantHash {
		t.Fatalf("batch hash = %x, want %x, err=%v", result.BatchHash, wantHash, err)
	}
	if !reflect.DeepEqual(f.request, requestBefore) || !reflect.DeepEqual(f.state.record, currentBefore) {
		t.Fatal("reconciler mutated request or current authorization")
	}
}

func TestAuthorizationRevocationReconcilerBatchIDStartsAtOne(t *testing.T) {
	f := newReconcileFixture(t)
	f.state.lastBatchFound = false
	result, err := f.service.Reconcile(context.Background(), f.request)
	if err != nil || result.BatchID != 1 {
		t.Fatalf("Reconcile() batch ID/error = %d/%v", result.BatchID, err)
	}
}

func TestAuthorizationRevocationReconcilerFailsClosed(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*reconcileFixture)
		wantPublish int
		wantConfirm int
	}{
		{name: "authorization reader", mutate: func(f *reconcileFixture) { f.state.recordErr = errors.New("read failed") }},
		{name: "policy id mismatch", mutate: func(f *reconcileFixture) { f.evaluator.decision.PolicyID = "other" }},
		{name: "policy version mismatch", mutate: func(f *reconcileFixture) { f.evaluator.decision.PolicyVersion = "8" }},
		{name: "noncanonical policy version", mutate: func(f *reconcileFixture) { f.evaluator.decision.PolicyVersion = "07" }},
		{name: "current issuer set reader", mutate: func(f *reconcileFixture) { f.state.issuerSetErr = errors.New("read failed") }},
		{name: "missing current issuer set", mutate: func(f *reconcileFixture) { f.state.issuerSetFound = false }},
		{name: "zero current issuer set", mutate: func(f *reconcileFixture) { f.state.issuerSetID = 0 }},
		{name: "last batch reader", mutate: func(f *reconcileFixture) { f.state.lastBatchErr = errors.New("read failed") }},
		{name: "last batch overflow", mutate: func(f *reconcileFixture) { f.state.lastBatchID = math.MaxUint64 }},
		{name: "signer failure", mutate: func(f *reconcileFixture) { f.signers[0].err = errors.New("sign failed") }},
		{name: "publisher failure", mutate: func(f *reconcileFixture) { f.publisher.err = errors.New("publish failed") }, wantPublish: 1},
		{name: "confirmer failure", mutate: func(f *reconcileFixture) { f.confirmer.err = errors.New("confirm failed") }, wantPublish: 1, wantConfirm: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReconcileFixture(t)
			tt.mutate(f)
			if _, err := f.service.Reconcile(context.Background(), f.request); err == nil {
				t.Fatal("Reconcile() error = nil")
			}
			if f.publisher.calls != tt.wantPublish || f.confirmer.calls != tt.wantConfirm {
				t.Fatalf("publish/confirm calls = %d/%d, want %d/%d", f.publisher.calls, f.confirmer.calls, tt.wantPublish, tt.wantConfirm)
			}
		})
	}
}

func TestAuthorizationRevocationReconcilerDependencyFailures(t *testing.T) {
	f := newReconcileFixture(t)
	f.source.err = errors.New("attributes unavailable")
	if _, err := f.service.Reconcile(context.Background(), f.request); err == nil || f.evaluator.calls != 0 || f.publisher.calls != 0 {
		t.Fatalf("attribute failure error/evaluate/publish = %v/%d/%d", err, f.evaluator.calls, f.publisher.calls)
	}

	f = newReconcileFixture(t)
	f.evaluator.err = errors.New("policy unavailable")
	if _, err := f.service.Reconcile(context.Background(), f.request); err == nil || f.state.authorizationCalls != 0 || f.publisher.calls != 0 {
		t.Fatalf("evaluator failure error/read/publish = %v/%d/%d", err, f.state.authorizationCalls, f.publisher.calls)
	}
}
