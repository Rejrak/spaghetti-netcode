package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"spaghetti/internal/remote/policy"
	"spaghetti/internal/user"
)

type issuerAttributeSource struct {
	attributes *user.Attributes
	err        error
	calls      int
	address    string
}

func (s *issuerAttributeSource) FetchAttributes(_ context.Context, address string) (*user.Attributes, error) {
	s.calls++
	s.address = address
	return s.attributes, s.err
}

type issuerPolicyEvaluator struct {
	decision policy.PolicyDecision
	err      error
	calls    int
	input    policy.PolicyInput
}

func (e *issuerPolicyEvaluator) Evaluate(_ context.Context, input policy.PolicyInput) (policy.PolicyDecision, error) {
	e.calls++
	e.input = input
	return e.decision, e.err
}

type issuerBatchSigner struct {
	issuerID string
	err      error
	calls    int
	message  []byte
}

func (s *issuerBatchSigner) IssuerID() string { return s.issuerID }
func (s *issuerBatchSigner) Sign(_ context.Context, message []byte) ([]byte, error) {
	s.calls++
	s.message = append([]byte(nil), message...)
	if s.err != nil {
		return nil, s.err
	}
	return bytes.Repeat([]byte{byte(len(s.issuerID))}, ed25519.SignatureSize), nil
}

type issuerBatchPublisher struct {
	result BroadcastResult
	err    error
	calls  int
	batch  AuthorizationBatch
}

func (p *issuerBatchPublisher) Publish(_ context.Context, batch AuthorizationBatch) (BroadcastResult, error) {
	p.calls++
	p.batch = cloneLogicalBatch(batch)
	return p.result, p.err
}

type issuerBatchConfirmer struct {
	result    CommitResult
	err       error
	calls     int
	batch     AuthorizationBatch
	broadcast BroadcastResult
}

func (c *issuerBatchConfirmer) WaitForCommit(_ context.Context, batch AuthorizationBatch, broadcast BroadcastResult) (CommitResult, error) {
	c.calls++
	c.batch = cloneLogicalBatch(batch)
	c.broadcast = broadcast
	return c.result, c.err
}

type issuerFixture struct {
	source    *issuerAttributeSource
	evaluator *issuerPolicyEvaluator
	signers   []*issuerBatchSigner
	publisher *issuerBatchPublisher
	confirmer *issuerBatchConfirmer
	logs      *bytes.Buffer
	service   *AuthorizationIssuer
	request   AuthorizationIssueRequest
}

func newIssuerFixture(t *testing.T) *issuerFixture {
	t.Helper()
	attributes := &user.Attributes{Perms: map[string]bool{"send": true}, Roles: []string{"operator"}}
	f := &issuerFixture{
		source: &issuerAttributeSource{attributes: attributes},
		evaluator: &issuerPolicyEvaluator{decision: policy.PolicyDecision{
			Allow: true, ReasonCode: policy.ReasonOK, Reason: "allowed",
			PolicyID: "policy-bank-send", PolicyVersion: "7",
		}},
		signers:   []*issuerBatchSigner{{issuerID: "issuer-beta"}, {issuerID: "issuer-alpha"}},
		publisher: &issuerBatchPublisher{result: BroadcastResult{TxHash: "ABC123"}},
		confirmer: &issuerBatchConfirmer{result: CommitResult{TxHash: "ABC123", Height: 88}},
		logs:      new(bytes.Buffer),
		request: AuthorizationIssueRequest{
			Facts: NormalizedMsgSendFacts{
				Subject: testSubject, MsgTypeURL: MsgSendTypeURL, Receiver: testReceiver,
				Denom: "uatom", Amount: "1000",
			},
			AuthorizationContext: TrustedAuthorizationContext{
				AuthorizationID: "authorization-1", IssuerSetID: 9,
				ValidFromHeight: 10, ValidUntilHeight: 100,
				AllowedDenom: "uatom", AllowedReceiver: testReceiver, MaxAmount: "5000",
			},
			BatchContext: TrustedBatchContext{
				ChainID: "alpha-1", BatchID: 42, PolicyID: "policy-bank-send",
				PolicyVersion: 7, PolicyHash: bytes.Repeat([]byte{0x2a}, 32), IssuerSetID: 9,
			},
		},
	}
	logger := slog.New(slog.NewJSONHandler(f.logs, nil))
	signers := []BatchSigner{f.signers[0], f.signers[1]}
	service, err := NewAuthorizationIssuer(f.source, f.evaluator, signers, f.publisher, f.confirmer, logger)
	if err != nil {
		t.Fatalf("NewAuthorizationIssuer() error = %v", err)
	}
	f.service = service
	return f
}

func TestAuthorizationIssuerIssueSuccess(t *testing.T) {
	f := newIssuerFixture(t)
	before := f.request
	before.BatchContext.PolicyHash = append([]byte(nil), f.request.BatchContext.PolicyHash...)

	result, err := f.service.Issue(context.Background(), f.request)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if f.source.calls != 1 || f.source.address != testSubject {
		t.Fatalf("FetchAttributes calls/address = %d/%q", f.source.calls, f.source.address)
	}
	if f.evaluator.calls != 1 || f.evaluator.input.Subject != testSubject || f.evaluator.input.Operation != MsgSendTypeURL {
		t.Fatalf("unexpected policy input: %+v", f.evaluator.input)
	}
	if f.evaluator.input.Attributes != f.source.attributes {
		t.Fatal("policy evaluator did not receive fetched attributes")
	}
	if result.AuthorizationRecord.PolicyID != "policy-bank-send" || result.AuthorizationRecord.PolicyVersion != 7 {
		t.Fatalf("unexpected record policy identity: %+v", result.AuthorizationRecord)
	}
	constraints := result.AuthorizationRecord.BankSendConstraints
	if constraints.Denom != "uatom" || constraints.Receiver != testReceiver || constraints.MaxAmount != "5000" {
		t.Fatalf("unexpected trusted constraints: %+v", constraints)
	}
	if len(f.publisher.batch.SignDoc.Records) != 1 || f.publisher.calls != 1 || f.confirmer.calls != 1 {
		t.Fatalf("unexpected pipeline calls/records: publish=%d confirm=%d records=%d", f.publisher.calls, f.confirmer.calls, len(f.publisher.batch.SignDoc.Records))
	}
	if f.signers[0].calls != 1 || f.signers[1].calls != 1 {
		t.Fatalf("signer calls = %d/%d", f.signers[0].calls, f.signers[1].calls)
	}
	if !reflect.DeepEqual(f.confirmer.batch, f.publisher.batch) || f.confirmer.broadcast != f.publisher.result {
		t.Fatal("confirmer did not receive the published batch and broadcast result")
	}
	_, wantHash, err := CanonicalBatchSignBytes(f.publisher.batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if result.BatchHash != wantHash || result.TxHash != "ABC123" || result.Height != 88 {
		t.Fatalf("unexpected issue result: %+v", result)
	}
	if !strings.Contains(f.logs.String(), `"msg":"policy_evaluated"`) ||
		!strings.Contains(f.logs.String(), `"outcome":"allow"`) ||
		strings.Contains(f.logs.String(), "operator") {
		t.Fatalf("unexpected policy log: %s", f.logs.String())
	}
	if !reflect.DeepEqual(f.request, before) {
		t.Fatal("Issue mutated caller request")
	}
}

func TestAuthorizationIssuerFailClosed(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(*issuerFixture)
		wantEvaluator   int
		wantSignerCalls int
		wantPublish     int
		wantConfirm     int
		wantDenied      bool
	}{
		{name: "attribute source error", mutate: func(f *issuerFixture) { f.source.err = errors.New("attributes unavailable") }, wantEvaluator: 0},
		{name: "evaluator error", mutate: func(f *issuerFixture) { f.evaluator.err = errors.New("policy unavailable") }, wantEvaluator: 1},
		{name: "policy deny", mutate: func(f *issuerFixture) {
			f.evaluator.decision.Allow = false
			f.evaluator.decision.ReasonCode = policy.ReasonPolicyMismatch
		}, wantEvaluator: 1, wantDenied: true},
		{name: "policy id mismatch", mutate: func(f *issuerFixture) { f.request.BatchContext.PolicyID = "other-policy" }, wantEvaluator: 1},
		{name: "policy version mismatch", mutate: func(f *issuerFixture) { f.request.BatchContext.PolicyVersion = 8 }, wantEvaluator: 1},
		{name: "malformed subject", mutate: func(f *issuerFixture) { f.request.Facts.Subject = "not-an-address" }, wantEvaluator: 1},
		{name: "invalid max amount", mutate: func(f *issuerFixture) { f.request.AuthorizationContext.MaxAmount = "0" }, wantEvaluator: 1},
		{name: "signer failure", mutate: func(f *issuerFixture) { f.signers[0].err = errors.New("signer unavailable") }, wantEvaluator: 1, wantSignerCalls: 1},
		{name: "publisher failure", mutate: func(f *issuerFixture) { f.publisher.err = errors.New("broadcast failed") }, wantEvaluator: 1, wantSignerCalls: 2, wantPublish: 1},
		{name: "confirmer failure", mutate: func(f *issuerFixture) { f.confirmer.err = errors.New("confirmation failed") }, wantEvaluator: 1, wantSignerCalls: 2, wantPublish: 1, wantConfirm: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			tt.mutate(f)
			_, err := f.service.Issue(context.Background(), f.request)
			if err == nil {
				t.Fatal("Issue() error = nil")
			}
			if f.evaluator.calls != tt.wantEvaluator || f.publisher.calls != tt.wantPublish || f.confirmer.calls != tt.wantConfirm {
				t.Fatalf("calls evaluator/publisher/confirmer = %d/%d/%d, want %d/%d/%d", f.evaluator.calls, f.publisher.calls, f.confirmer.calls, tt.wantEvaluator, tt.wantPublish, tt.wantConfirm)
			}
			signerCalls := f.signers[0].calls + f.signers[1].calls
			if signerCalls != tt.wantSignerCalls {
				t.Fatalf("signer calls = %d, want %d", signerCalls, tt.wantSignerCalls)
			}
			var denied *PolicyDeniedError
			if errors.As(err, &denied) != tt.wantDenied {
				t.Fatalf("PolicyDeniedError = %v, want %v (error %v)", errors.As(err, &denied), tt.wantDenied, err)
			}
			if tt.wantDenied {
				logOutput := f.logs.String()
				if !strings.Contains(logOutput, `"outcome":"deny"`) || strings.Contains(logOutput, "authorization_built") || strings.Contains(logOutput, "batch_") {
					t.Fatalf("unexpected denial log: %s", logOutput)
				}
			}
		})
	}
}

func TestNewAuthorizationIssuerRejectsMissingDependencies(t *testing.T) {
	f := newIssuerFixture(t)
	tests := []struct {
		name      string
		source    AttributeSource
		evaluator policy.PolicyEvaluator
		signers   []BatchSigner
		publisher BatchPublisher
		confirmer BatchCommitConfirmer
	}{
		{name: "source", evaluator: f.evaluator, signers: []BatchSigner{f.signers[0]}, publisher: f.publisher, confirmer: f.confirmer},
		{name: "evaluator", source: f.source, signers: []BatchSigner{f.signers[0]}, publisher: f.publisher, confirmer: f.confirmer},
		{name: "signers", source: f.source, evaluator: f.evaluator, publisher: f.publisher, confirmer: f.confirmer},
		{name: "publisher", source: f.source, evaluator: f.evaluator, signers: []BatchSigner{f.signers[0]}, confirmer: f.confirmer},
		{name: "confirmer", source: f.source, evaluator: f.evaluator, signers: []BatchSigner{f.signers[0]}, publisher: f.publisher},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAuthorizationIssuer(tt.source, tt.evaluator, tt.signers, tt.publisher, tt.confirmer, nil); err == nil {
				t.Fatal("NewAuthorizationIssuer() error = nil")
			}
		})
	}
}

func TestAuthorizationIssuerRejectsNilOrCancelledContext(t *testing.T) {
	f := newIssuerFixture(t)
	if _, err := f.service.Issue(nil, f.request); err == nil || f.source.calls != 0 {
		t.Fatalf("nil context error/calls = %v/%d", err, f.source.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.service.Issue(ctx, f.request); !errors.Is(err, context.Canceled) || f.source.calls != 0 {
		t.Fatalf("cancelled context error/calls = %v/%d", err, f.source.calls)
	}
}
