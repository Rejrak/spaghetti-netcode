package authorization

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"sort"
	"strings"

	authzpb "spaghetti/internal/authorization/pb"

	"google.golang.org/protobuf/proto"
)

const MsgBatchUpsertAuthorizationsTypeURL = "/alpha.authzattrs.v1.MsgBatchUpsertAuthorizations"

// ToProtoAuthorizationBatch creates a detached, canonically ordered wire batch.
func ToProtoAuthorizationBatch(batch AuthorizationBatch) (*authzpb.AuthorizationBatch, error) {
	canonical, err := CanonicalizeBatchSignDoc(batch.SignDoc)
	if err != nil {
		return nil, err
	}
	if len(batch.Signatures) == 0 {
		return nil, fmt.Errorf("at least one batch signature is required")
	}

	signatures := append([]BatchSignature(nil), batch.Signatures...)
	seen := make(map[string]struct{}, len(signatures))
	for _, signature := range signatures {
		if strings.TrimSpace(signature.IssuerID) == "" {
			return nil, fmt.Errorf("empty issuer id")
		}
		if len(signature.Signature) != ed25519.SignatureSize {
			return nil, fmt.Errorf("invalid signature length for issuer %q", signature.IssuerID)
		}
		if _, exists := seen[signature.IssuerID]; exists {
			return nil, fmt.Errorf("duplicate issuer id")
		}
		seen[signature.IssuerID] = struct{}{}
	}
	sort.Slice(signatures, func(i, j int) bool {
		return bytes.Compare([]byte(signatures[i].IssuerID), []byte(signatures[j].IssuerID)) < 0
	})

	protoSignatures := make([]*authzpb.BatchSignature, len(signatures))
	for i, signature := range signatures {
		protoSignatures[i] = &authzpb.BatchSignature{
			IssuerId:  signature.IssuerID,
			Signature: append([]byte(nil), signature.Signature...),
		}
	}
	return &authzpb.AuthorizationBatch{
		SignDoc:    toProtoBatchSignDoc(canonical),
		Signatures: protoSignatures,
	}, nil
}

// BuildBatchUpsertMessage wraps a signed batch for a permissionless Cosmos
// submitter without adding the submitter to issuer-signed data.
func BuildBatchUpsertMessage(submitter string, batch AuthorizationBatch) (*authzpb.MsgBatchUpsertAuthorizations, error) {
	if strings.TrimSpace(submitter) == "" {
		return nil, fmt.Errorf("empty submitter")
	}
	if err := validateAccountAddress(submitter); err != nil {
		return nil, fmt.Errorf("invalid submitter: %w", err)
	}
	protoBatch, err := ToProtoAuthorizationBatch(batch)
	if err != nil {
		return nil, err
	}
	return &authzpb.MsgBatchUpsertAuthorizations{Submitter: submitter, Batch: protoBatch}, nil
}

func MarshalBatchUpsertMessage(msg *authzpb.MsgBatchUpsertAuthorizations) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("nil batch upsert message")
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal batch upsert message: %w", err)
	}
	return encoded, nil
}

func toProtoBatchSignDoc(signDoc BatchSignDoc) *authzpb.AuthorizationBatchSignDoc {
	records := make([]*authzpb.AuthorizationRecord, len(signDoc.Records))
	for i, record := range signDoc.Records {
		records[i] = &authzpb.AuthorizationRecord{
			AuthorizationId:  record.AuthorizationID,
			Subject:          record.Subject,
			MsgTypeUrl:       record.MsgTypeURL,
			PolicyId:         record.PolicyID,
			PolicyVersion:    record.PolicyVersion,
			IssuerSetId:      record.IssuerSetID,
			ValidFromHeight:  record.ValidFromHeight,
			ValidUntilHeight: record.ValidUntilHeight,
			Revoked:          record.Revoked,
			BankSendConstraints: &authzpb.BankSendConstraints{
				Denom:     record.BankSendConstraints.Denom,
				Receiver:  record.BankSendConstraints.Receiver,
				MaxAmount: record.BankSendConstraints.MaxAmount,
			},
		}
	}
	return &authzpb.AuthorizationBatchSignDoc{
		Domain:        signDoc.Domain,
		ChainId:       signDoc.ChainID,
		BatchId:       signDoc.BatchID,
		PolicyId:      signDoc.PolicyID,
		PolicyVersion: signDoc.PolicyVersion,
		PolicyHash:    append([]byte(nil), signDoc.PolicyHash...),
		IssuerSetId:   signDoc.IssuerSetID,
		Records:       records,
	}
}
