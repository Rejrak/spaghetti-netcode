package authorization

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"spaghetti/internal/remote/policy"
)

type AuthorizationReconcileStatus string

const (
	ReconcileNoopPolicyAllows   AuthorizationReconcileStatus = "NOOP_POLICY_ALLOWS"
	ReconcileNoopNoCurrent      AuthorizationReconcileStatus = "NOOP_NO_CURRENT"
	ReconcileNoopAlreadyRevoked AuthorizationReconcileStatus = "NOOP_ALREADY_REVOKED"
	ReconcileRevoked            AuthorizationReconcileStatus = "REVOKED"
)

type AuthorizationReconcileRequest struct {
	Subject    string
	MsgTypeURL string
	ChainID    string
	PolicyHash []byte
}

type AuthorizationReconcileResult struct {
	Status          AuthorizationReconcileStatus
	AuthorizationID string
	BatchID         uint64
	BatchHash       [sha256.Size]byte
	TxHash          string
	Height          int64
}

type AuthorizationRevocationReconciler struct {
	attributes AttributeSource
	evaluator  policy.PolicyEvaluator
	state      AuthorizationStateReader
	signers    []BatchSigner
	publisher  BatchPublisher
	confirmer  BatchCommitConfirmer
	logger     *slog.Logger
}

func NewAuthorizationRevocationReconciler(
	attributes AttributeSource,
	evaluator policy.PolicyEvaluator,
	state AuthorizationStateReader,
	signers []BatchSigner,
	publisher BatchPublisher,
	confirmer BatchCommitConfirmer,
	logger *slog.Logger,
) (*AuthorizationRevocationReconciler, error) {
	if isNilDependency(attributes) {
		return nil, fmt.Errorf("nil attribute source")
	}
	if isNilDependency(evaluator) {
		return nil, fmt.Errorf("nil policy evaluator")
	}
	if isNilDependency(state) {
		return nil, fmt.Errorf("nil authorization state reader")
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
	return &AuthorizationRevocationReconciler{
		attributes: attributes,
		evaluator:  evaluator,
		state:      state,
		signers:    append([]BatchSigner(nil), signers...),
		publisher:  publisher,
		confirmer:  confirmer,
		logger:     logger,
	}, nil
}

func (r *AuthorizationRevocationReconciler) Reconcile(ctx context.Context, request AuthorizationReconcileRequest) (AuthorizationReconcileResult, error) {
	if ctx == nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("nil context")
	}
	if err := ctx.Err(); err != nil {
		return AuthorizationReconcileResult{}, err
	}
	if err := validateAccountAddress(request.Subject); err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("invalid subject: %w", err)
	}
	if request.MsgTypeURL != MsgSendTypeURL {
		return AuthorizationReconcileResult{}, fmt.Errorf("unsupported message type")
	}
	if strings.TrimSpace(request.ChainID) == "" {
		return AuthorizationReconcileResult{}, fmt.Errorf("empty chain id")
	}
	if len(request.PolicyHash) != sha256.Size {
		return AuthorizationReconcileResult{}, fmt.Errorf("policy hash must be 32 bytes")
	}

	attributes, err := r.attributes.FetchAttributes(ctx, request.Subject)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("fetch attributes: %w", err)
	}
	decision, err := r.evaluator.Evaluate(ctx, policy.PolicyInput{
		Subject:    request.Subject,
		Operation:  request.MsgTypeURL,
		Attributes: attributes,
	})
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("evaluate policy: %w", err)
	}
	LogPolicyEvaluated(ctx, r.logger, decision)
	if decision.Allow {
		return AuthorizationReconcileResult{Status: ReconcileNoopPolicyAllows}, nil
	}

	current, found, err := r.state.Authorization(ctx, request.Subject, request.MsgTypeURL)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("read current authorization: %w", err)
	}
	if !found {
		return AuthorizationReconcileResult{Status: ReconcileNoopNoCurrent}, nil
	}
	if current.Revoked {
		return AuthorizationReconcileResult{Status: ReconcileNoopAlreadyRevoked}, nil
	}
	if decision.PolicyID != current.PolicyID {
		return AuthorizationReconcileResult{}, fmt.Errorf("policy id mismatch between decision and current authorization")
	}
	if !amountPattern.MatchString(decision.PolicyVersion) {
		return AuthorizationReconcileResult{}, fmt.Errorf("invalid policy decision version")
	}
	policyVersion, err := strconv.ParseUint(decision.PolicyVersion, 10, 64)
	if err != nil || policyVersion != current.PolicyVersion {
		return AuthorizationReconcileResult{}, fmt.Errorf("policy version mismatch between decision and current authorization")
	}

	issuerSetID, found, err := r.state.CurrentIssuerSet(ctx, current.PolicyID, current.MsgTypeURL)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("read current issuer set: %w", err)
	}
	if !found || issuerSetID == 0 {
		return AuthorizationReconcileResult{}, fmt.Errorf("current issuer set not found")
	}
	lastBatchID, found, err := r.state.LastAppliedBatchID(ctx, issuerSetID)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("read last applied batch id: %w", err)
	}
	batchID := uint64(1)
	if found {
		if lastBatchID == math.MaxUint64 {
			return AuthorizationReconcileResult{}, fmt.Errorf("last applied batch id overflow")
		}
		batchID = lastBatchID + 1
	}

	revoked := cloneAuthorizationRecord(current)
	revoked.Revoked = true
	revoked.IssuerSetID = issuerSetID
	LogAuthorizationBuilt(ctx, r.logger, revoked)
	signDoc, err := BuildBatchSignDoc(TrustedBatchContext{
		ChainID:       request.ChainID,
		BatchID:       batchID,
		PolicyID:      current.PolicyID,
		PolicyVersion: current.PolicyVersion,
		PolicyHash:    append([]byte(nil), request.PolicyHash...),
		IssuerSetID:   issuerSetID,
	}, []AuthorizationRecord{revoked})
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("build revocation batch sign document: %w", err)
	}
	_, batchHash, err := CanonicalBatchSignBytes(signDoc)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("hash revocation batch: %w", err)
	}
	LogBatchBuilt(ctx, r.logger, signDoc, batchHash)
	batch, signedBatchHash, err := SignAuthorizationBatch(ctx, signDoc, r.signers)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("sign revocation batch: %w", err)
	}
	if signedBatchHash != batchHash {
		return AuthorizationReconcileResult{}, fmt.Errorf("canonical batch hash changed during signing")
	}
	LogBatchSigned(ctx, r.logger, batch, signedBatchHash)
	broadcast, err := r.publisher.Publish(ctx, batch)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("publish revocation batch: %w", err)
	}
	commit, err := r.confirmer.WaitForCommit(ctx, batch, broadcast)
	if err != nil {
		return AuthorizationReconcileResult{}, fmt.Errorf("confirm revocation batch: %w", err)
	}
	return AuthorizationReconcileResult{
		Status:          ReconcileRevoked,
		AuthorizationID: revoked.AuthorizationID,
		BatchID:         batchID,
		BatchHash:       signedBatchHash,
		TxHash:          commit.TxHash,
		Height:          commit.Height,
	}, nil
}
