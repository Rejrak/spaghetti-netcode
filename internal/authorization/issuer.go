package authorization

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"

	"spaghetti/internal/remote/policy"
	"spaghetti/internal/user"
)

// AttributeSource supplies normalized off-chain attributes for policy evaluation.
type AttributeSource interface {
	FetchAttributes(context.Context, string) (*user.Attributes, error)
}

type AuthorizationIssueRequest struct {
	Facts                NormalizedMsgSendFacts
	AuthorizationContext TrustedAuthorizationContext
	BatchContext         TrustedBatchContext
}

type AuthorizationIssueResult struct {
	AuthorizationRecord AuthorizationRecord
	BatchHash           [sha256.Size]byte
	TxHash              string
	Height              int64
}

type PolicyDeniedError struct {
	ReasonCode string
	Reason     string
}

func (e *PolicyDeniedError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("policy denied authorization: %s", e.ReasonCode)
	}
	return fmt.Sprintf("policy denied authorization: %s: %s", e.ReasonCode, e.Reason)
}

// AuthorizationIssuer coordinates one record through signing, broadcast, and
// commit confirmation. Policy and trusted metadata remain separate inputs.
type AuthorizationIssuer struct {
	attributes AttributeSource
	evaluator  policy.PolicyEvaluator
	signers    []BatchSigner
	publisher  BatchPublisher
	confirmer  BatchCommitConfirmer
	logger     *slog.Logger
}

func NewAuthorizationIssuer(
	attributes AttributeSource,
	evaluator policy.PolicyEvaluator,
	signers []BatchSigner,
	publisher BatchPublisher,
	confirmer BatchCommitConfirmer,
	logger *slog.Logger,
) (*AuthorizationIssuer, error) {
	if isNilDependency(attributes) {
		return nil, fmt.Errorf("nil attribute source")
	}
	if isNilDependency(evaluator) {
		return nil, fmt.Errorf("nil policy evaluator")
	}
	if len(signers) == 0 {
		return nil, fmt.Errorf("at least one batch signer is required")
	}
	for _, signer := range signers {
		if isNilDependency(signer) {
			return nil, fmt.Errorf("nil batch signer")
		}
	}
	if isNilDependency(publisher) {
		return nil, fmt.Errorf("nil batch publisher")
	}
	if isNilDependency(confirmer) {
		return nil, fmt.Errorf("nil batch commit confirmer")
	}
	return &AuthorizationIssuer{
		attributes: attributes,
		evaluator:  evaluator,
		signers:    append([]BatchSigner(nil), signers...),
		publisher:  publisher,
		confirmer:  confirmer,
		logger:     logger,
	}, nil
}

func (s *AuthorizationIssuer) Issue(ctx context.Context, request AuthorizationIssueRequest) (AuthorizationIssueResult, error) {
	if ctx == nil {
		return AuthorizationIssueResult{}, fmt.Errorf("nil context")
	}
	if err := ctx.Err(); err != nil {
		return AuthorizationIssueResult{}, err
	}

	attributes, err := s.attributes.FetchAttributes(ctx, request.Facts.Subject)
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("fetch attributes: %w", err)
	}
	decision, err := s.evaluator.Evaluate(ctx, policy.PolicyInput{
		Subject:    request.Facts.Subject,
		Operation:  request.Facts.MsgTypeURL,
		Attributes: attributes,
	})
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("evaluate policy: %w", err)
	}
	LogPolicyEvaluated(ctx, s.logger, decision)
	if !decision.Allow {
		return AuthorizationIssueResult{}, &PolicyDeniedError{ReasonCode: decision.ReasonCode, Reason: decision.Reason}
	}
	if decision.PolicyID != request.BatchContext.PolicyID {
		return AuthorizationIssueResult{}, fmt.Errorf("policy id mismatch between decision and trusted batch context")
	}
	policyVersion, err := strconv.ParseUint(decision.PolicyVersion, 10, 64)
	if err != nil || policyVersion == 0 || policyVersion != request.BatchContext.PolicyVersion {
		return AuthorizationIssueResult{}, fmt.Errorf("policy version mismatch between decision and trusted batch context")
	}

	record, err := BuildAuthorizationRecord(request.Facts, decision, request.AuthorizationContext)
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("build authorization record: %w", err)
	}
	LogAuthorizationBuilt(ctx, s.logger, record)
	signDoc, err := BuildBatchSignDoc(request.BatchContext, []AuthorizationRecord{record})
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("build batch sign document: %w", err)
	}
	_, batchHash, err := CanonicalBatchSignBytes(signDoc)
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("hash authorization batch: %w", err)
	}
	LogBatchBuilt(ctx, s.logger, signDoc, batchHash)
	batch, signedBatchHash, err := SignAuthorizationBatch(ctx, signDoc, s.signers)
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("sign authorization batch: %w", err)
	}
	if signedBatchHash != batchHash {
		return AuthorizationIssueResult{}, fmt.Errorf("canonical batch hash changed during signing")
	}
	LogBatchSigned(ctx, s.logger, batch, signedBatchHash)
	broadcast, err := s.publisher.Publish(ctx, batch)
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("publish authorization batch: %w", err)
	}
	commit, err := s.confirmer.WaitForCommit(ctx, batch, broadcast)
	if err != nil {
		return AuthorizationIssueResult{}, fmt.Errorf("confirm authorization batch: %w", err)
	}
	return AuthorizationIssueResult{
		AuthorizationRecord: record,
		BatchHash:           signedBatchHash,
		TxHash:              commit.TxHash,
		Height:              commit.Height,
	}, nil
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
