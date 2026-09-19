package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"spaghetti/internal/observability"
)

// LogAuthorizationBuilt emits the Protocol V1.1 middleware event without
// exposing the subject address itself.
func LogAuthorizationBuilt(ctx context.Context, logger *slog.Logger, record AuthorizationRecord) {
	if logger == nil {
		logger = slog.Default()
	}
	subjectHash := sha256.Sum256([]byte(record.Subject))
	logger.InfoContext(ctx, observability.EventAuthorizationBuilt,
		"component", "authorization_builder",
		"operation", "build_authorization_record",
		"outcome", "success",
		"authorization_id", record.AuthorizationID,
		"subject_hash", hex.EncodeToString(subjectHash[:]),
		"msg_type", record.MsgTypeURL,
		"policy_id", record.PolicyID,
		"policy_version", record.PolicyVersion,
		"valid_from_height", record.ValidFromHeight,
		"valid_until_height", record.ValidUntilHeight,
	)
}

func LogBatchBuilt(ctx context.Context, logger *slog.Logger, signDoc BatchSignDoc, batchHash [sha256.Size]byte) {
	logBatchEvent(ctx, logger, observability.EventBatchBuilt, signDoc, len(signDoc.Records), 0, batchHash)
}

func LogBatchSigned(ctx context.Context, logger *slog.Logger, batch AuthorizationBatch, batchHash [sha256.Size]byte) {
	logBatchEvent(ctx, logger, observability.EventBatchSigned, batch.SignDoc, 0, len(batch.Signatures), batchHash)
}

func logBatchEvent(ctx context.Context, logger *slog.Logger, event string, signDoc BatchSignDoc, recordCount, signatureCount int, batchHash [sha256.Size]byte) {
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"component", "authorization_batch",
		"batch_id", signDoc.BatchID,
		"policy_id", signDoc.PolicyID,
		"policy_version", signDoc.PolicyVersion,
		"issuer_set_id", signDoc.IssuerSetID,
		"batch_hash", hex.EncodeToString(batchHash[:]),
	}
	if recordCount > 0 {
		attrs = append(attrs, "record_count", recordCount)
	}
	if signatureCount > 0 {
		attrs = append(attrs, "signature_count", signatureCount)
	}
	logger.InfoContext(ctx, event, attrs...)
}
