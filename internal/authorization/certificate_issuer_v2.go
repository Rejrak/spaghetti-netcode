package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"strconv"

	"spaghetti/internal/remote/policy"
)

// CertificateIssueRequestV2 contains only transaction facts proposed by a client.
type CertificateIssueRequestV2 struct {
	Subject       string
	Receiver      string
	Denom         string
	Amount        string
	TimeoutHeight uint64
	Memo          string
	FeeAmount     []FeeCoinV2
	GasLimit      uint64
}

// AlphaAccountStateV2 is obtained from Alpha, never from a certificate requester.
type AlphaAccountStateV2 struct {
	ChainID       string
	AccountNumber uint64
	Sequence      uint64
	CurrentHeight int64
}

type AlphaAccountStateProvider interface {
	AccountState(context.Context, string) (AlphaAccountStateV2, error)
}

// CertificateIssuerV2Config is trusted deployment configuration. PolicyHash is
// the raw SHA-256 hash of a V2 policy descriptor selected by the deployment;
// it is not the V1 KeycloakPolicyHash and is never taken from request facts.
type CertificateIssuerV2Config struct {
	ChainID        string
	PolicyID       string
	PolicyVersion  uint64
	PolicyHash     []byte
	IssuerSetID    uint64
	LifetimeBlocks int64
}

type CertificateIssueResultV2 struct {
	Intent           AuthorizationIntentV2
	Certificate      AuthorizationCertificateV2
	CertificateBytes []byte
	Digest           [sha256.Size]byte
	AccountState     AlphaAccountStateV2
}

type CertificateIssuerV2 struct {
	attributes AttributeSource
	evaluator  policy.PolicyEvaluator
	state      AlphaAccountStateProvider
	signers    []CertificateSignerV2
	config     CertificateIssuerV2Config
	logger     *slog.Logger
}

func NewCertificateIssuerV2(attributes AttributeSource, evaluator policy.PolicyEvaluator, state AlphaAccountStateProvider, signers []CertificateSignerV2, config CertificateIssuerV2Config, logger *slog.Logger) (*CertificateIssuerV2, error) {
	if isNilDependency(attributes) || isNilDependency(evaluator) || isNilDependency(state) {
		return nil, fmt.Errorf("missing V2 issuer dependency")
	}
	if len(signers) == 0 || len(signers) > maxCertificateSignaturesV2 {
		return nil, fmt.Errorf("V2 signer count must be 1..16")
	}
	for _, signer := range signers {
		if isNilDependency(signer) {
			return nil, fmt.Errorf("nil V2 signer")
		}
	}
	if config.ChainID == "" || config.PolicyID == "" || config.PolicyVersion == 0 ||
		len(config.PolicyHash) != sha256.Size || config.IssuerSetID == 0 || config.LifetimeBlocks <= 0 {
		return nil, fmt.Errorf("invalid trusted V2 issuer configuration")
	}
	config.PolicyHash = append([]byte(nil), config.PolicyHash...)
	return &CertificateIssuerV2{
		attributes: attributes, evaluator: evaluator, state: state,
		signers: append([]CertificateSignerV2(nil), signers...), config: config, logger: logger,
	}, nil
}

func (s *CertificateIssuerV2) Issue(ctx context.Context, request CertificateIssueRequestV2) (CertificateIssueResultV2, error) {
	if ctx == nil {
		return CertificateIssueResultV2{}, fmt.Errorf("nil V2 issuance context")
	}
	if err := ctx.Err(); err != nil {
		return CertificateIssueResultV2{}, err
	}
	intent := AuthorizationIntentV2{
		ChainID: s.config.ChainID, Subject: request.Subject, Receiver: request.Receiver,
		Denom: request.Denom, Amount: request.Amount, TimeoutHeight: request.TimeoutHeight,
		Memo: request.Memo, FeeAmount: append([]FeeCoinV2(nil), request.FeeAmount...), GasLimit: request.GasLimit,
	}
	// Reuse the V2 canonicalizer to validate client facts before any external I/O.
	if _, err := BuildCertificateSignDocV2(intent, TrustedCertificateContextV2{
		ChainID: s.config.ChainID, PolicyID: s.config.PolicyID,
		PolicyVersion: s.config.PolicyVersion, PolicyHash: s.config.PolicyHash,
		IssuerSetID: s.config.IssuerSetID, ValidFromHeight: 1, ValidUntilHeight: 1,
	}); err != nil {
		return CertificateIssueResultV2{}, fmt.Errorf("invalid V2 transaction facts: %w", err)
	}
	attributes, err := s.attributes.FetchAttributes(ctx, request.Subject)
	if err != nil {
		return CertificateIssueResultV2{}, fmt.Errorf("fetch V2 attributes: %w", err)
	}
	if attributes == nil {
		return CertificateIssueResultV2{}, fmt.Errorf("V2 attributes not found")
	}
	decision, err := s.evaluator.Evaluate(ctx, policy.PolicyInput{
		Subject: request.Subject, Operation: MsgSendTypeURL, Attributes: attributes,
	})
	if err != nil {
		return CertificateIssueResultV2{}, fmt.Errorf("evaluate V2 policy: %w", err)
	}
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	outcome := "deny"
	if decision.Allow {
		outcome = "allow"
	}
	logger.InfoContext(ctx, "v2_policy_evaluated", "component", "v2_certificate_issuer", "outcome", outcome,
		"reason_code", decision.ReasonCode, "subject", request.Subject,
		"policy_id", decision.PolicyID, "policy_version", decision.PolicyVersion)
	if !decision.Allow {
		return CertificateIssueResultV2{}, &PolicyDeniedError{ReasonCode: decision.ReasonCode, Reason: decision.Reason}
	}
	version, err := strconv.ParseUint(decision.PolicyVersion, 10, 64)
	if decision.PolicyID != s.config.PolicyID || err != nil || version == 0 ||
		strconv.FormatUint(version, 10) != decision.PolicyVersion || version != s.config.PolicyVersion {
		return CertificateIssueResultV2{}, fmt.Errorf("V2 policy decision differs from trusted configuration")
	}
	state, err := s.state.AccountState(ctx, request.Subject)
	if err != nil {
		return CertificateIssueResultV2{}, fmt.Errorf("query trusted Alpha account state: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return CertificateIssueResultV2{}, err
	}
	if state.ChainID == "" || state.ChainID != s.config.ChainID || state.CurrentHeight <= 0 ||
		s.config.LifetimeBlocks-1 > math.MaxInt64-state.CurrentHeight {
		return CertificateIssueResultV2{}, fmt.Errorf("invalid or mismatched trusted Alpha state")
	}
	intent.ChainID = state.ChainID
	intent.AccountNumber = state.AccountNumber
	intent.Sequence = state.Sequence
	trusted := TrustedCertificateContextV2{
		ChainID: state.ChainID, PolicyID: s.config.PolicyID, PolicyVersion: s.config.PolicyVersion,
		PolicyHash: s.config.PolicyHash, IssuerSetID: s.config.IssuerSetID,
		ValidFromHeight:  state.CurrentHeight,
		ValidUntilHeight: state.CurrentHeight + s.config.LifetimeBlocks - 1,
	}
	certificate, _, digest, err := BuildAuthorizationCertificateV2(ctx, intent, trusted, s.signers)
	if err != nil {
		return CertificateIssueResultV2{}, fmt.Errorf("build V2 certificate: %w", err)
	}
	encoded, err := MarshalAuthorizationCertificateV2(certificate)
	if err != nil {
		return CertificateIssueResultV2{}, fmt.Errorf("marshal V2 certificate: %w", err)
	}
	logger.InfoContext(ctx, "v2_certificate_built", "component", "v2_certificate_issuer",
		"subject", certificate.SignDoc.Intent.Subject, "sequence", certificate.SignDoc.Intent.Sequence,
		"policy_id", certificate.SignDoc.PolicyID, "policy_version", certificate.SignDoc.PolicyVersion,
		"issuer_set_id", certificate.SignDoc.IssuerSetID, "certificate_digest", hex.EncodeToString(digest[:]))
	logger.InfoContext(ctx, "v2_certificate_signed", "component", "v2_certificate_issuer",
		"subject", certificate.SignDoc.Intent.Subject, "sequence", certificate.SignDoc.Intent.Sequence,
		"policy_id", certificate.SignDoc.PolicyID, "policy_version", certificate.SignDoc.PolicyVersion,
		"issuer_set_id", certificate.SignDoc.IssuerSetID,
		"signature_count", len(certificate.Signatures), "certificate_digest", hex.EncodeToString(digest[:]))
	return CertificateIssueResultV2{
		Intent: certificate.SignDoc.Intent, Certificate: certificate,
		CertificateBytes: encoded, Digest: digest, AccountState: state,
	}, nil
}
