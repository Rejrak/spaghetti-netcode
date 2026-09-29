package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cosmos/cosmos-sdk/client"
)

type V2CertificateIssuer interface {
	Issue(context.Context, CertificateIssueRequestV2) (CertificateIssueResultV2, error)
}

type V2SubmissionResult struct {
	Subject           string
	Sequence          uint64
	CertificateDigest [sha256.Size]byte
	TxHash            string
	Height            int64
	Code              uint32
}

type V2OneTxService struct {
	issuer      V2CertificateIssuer
	txConfig    client.TxConfig
	account     CosmosAccountSignerV2
	broadcaster V2TxBroadcaster
	confirmer   V2TxConfirmer
	build       func(context.Context, client.TxConfig, CertificateIssueResultV2, CosmosAccountSignerV2) (SignedTransactionV2, error)
	logger      *slog.Logger
}

func NewV2OneTxService(issuer V2CertificateIssuer, txConfig client.TxConfig, account CosmosAccountSignerV2, broadcaster V2TxBroadcaster, confirmer V2TxConfirmer, logger *slog.Logger) (*V2OneTxService, error) {
	if isNilDependency(issuer) || isNilDependency(txConfig) || account.Keyring == nil || account.Name == "" ||
		isNilDependency(broadcaster) || isNilDependency(confirmer) {
		return nil, fmt.Errorf("missing V2 one-transaction dependency")
	}
	return &V2OneTxService{
		issuer: issuer, txConfig: txConfig, account: account, broadcaster: broadcaster,
		confirmer: confirmer, build: BuildSignedV2Transaction, logger: logger,
	}, nil
}

// IssueAndSubmitV2 never allocates another sequence or sends an authorization
// batch. A stale sequence requires the caller to start a fresh issuance.
func (s *V2OneTxService) IssueAndSubmitV2(ctx context.Context, request CertificateIssueRequestV2) (V2SubmissionResult, error) {
	if ctx == nil {
		return V2SubmissionResult{}, fmt.Errorf("nil V2 transaction context")
	}
	if err := ctx.Err(); err != nil {
		return V2SubmissionResult{}, err
	}
	issued, err := s.issuer.Issue(ctx, request)
	if err != nil {
		return V2SubmissionResult{}, fmt.Errorf("issue V2 certificate: %w", err)
	}
	signed, err := s.build(ctx, s.txConfig, issued, s.account)
	if err != nil {
		return V2SubmissionResult{}, fmt.Errorf("build signed V2 transaction: %w", err)
	}
	if len(signed.TxBytes) == 0 || signed.CertificateDigest != issued.Digest ||
		signed.Subject != issued.Intent.Subject || signed.Sequence != issued.Intent.Sequence {
		return V2SubmissionResult{}, fmt.Errorf("signed V2 transaction differs from issuance")
	}
	result := V2SubmissionResult{Subject: signed.Subject, Sequence: signed.Sequence, CertificateDigest: signed.CertificateDigest}
	logV2TxEvent(ctx, s.logger, "v2_tx_built", "", result.CertificateDigest,
		"subject", result.Subject, "sequence", result.Sequence)
	broadcast, err := s.broadcaster.Broadcast(ctx, signed.TxBytes)
	if err != nil {
		logV2TxEvent(ctx, s.logger, "v2_tx_failed", "", result.CertificateDigest, "phase", "broadcast")
		return result, fmt.Errorf("broadcast V2 transaction: %w", err)
	}
	expectedHash := sha256.Sum256(signed.TxBytes)
	if !strings.EqualFold(broadcast.TxHash, hex.EncodeToString(expectedHash[:])) {
		return result, fmt.Errorf("broadcast V2 tx hash differs from signed bytes")
	}
	result.TxHash = broadcast.TxHash
	logV2TxEvent(ctx, s.logger, "v2_tx_broadcast", result.TxHash, result.CertificateDigest)
	included, err := s.confirmer.WaitForInclusion(ctx, broadcast.TxHash)
	result.Height, result.Code = included.Height, included.Code
	if err != nil {
		logV2TxEvent(ctx, s.logger, "v2_tx_failed", result.TxHash, result.CertificateDigest,
			"phase", "confirmation", "code", result.Code)
		return result, fmt.Errorf("confirm V2 transaction: %w", err)
	}
	if !strings.EqualFold(included.TxHash, broadcast.TxHash) || included.Height <= 0 || included.Code != 0 {
		logV2TxEvent(ctx, s.logger, "v2_tx_failed", result.TxHash, result.CertificateDigest,
			"phase", "confirmation", "code", result.Code)
		return result, fmt.Errorf("V2 inclusion result does not match broadcast")
	}
	result.TxHash = included.TxHash
	logV2TxEvent(ctx, s.logger, "v2_tx_confirmed", result.TxHash, result.CertificateDigest,
		"height", result.Height, "code", result.Code)
	return result, nil
}
