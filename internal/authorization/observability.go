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
