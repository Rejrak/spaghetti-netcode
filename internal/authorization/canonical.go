package authorization

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	authzpb "spaghetti/internal/authorization/pb"

	"google.golang.org/protobuf/proto"
)

const BatchDomain = "alpha.authzattrs.batch.v1"

type BatchSignDoc struct {
	Domain        string
	ChainID       string
	BatchID       uint64
	PolicyID      string
	PolicyVersion uint64
	PolicyHash    []byte
	IssuerSetID   uint64
	Records       []AuthorizationRecord
}

// CanonicalBatchSignBytes validates and rebuilds a canonical Protocol V1.2.1
// sign document without retaining or mutating caller-owned slices.
func CanonicalBatchSignBytes(input BatchSignDoc) ([]byte, [sha256.Size]byte, error) {
	if input.Domain != BatchDomain {
		return nil, [sha256.Size]byte{}, fmt.Errorf("invalid batch domain")
	}
	if input.ChainID == "" || input.BatchID == 0 || strings.TrimSpace(input.PolicyID) == "" || input.PolicyVersion == 0 || len(input.PolicyHash) != sha256.Size || input.IssuerSetID == 0 || len(input.Records) == 0 {
		return nil, [sha256.Size]byte{}, fmt.Errorf("invalid batch sign document")
	}

	records := append([]AuthorizationRecord(nil), input.Records...)
	for _, record := range records {
		if err := validateCanonicalRecord(record); err != nil {
			return nil, [sha256.Size]byte{}, err
		}
		if record.PolicyID != input.PolicyID || record.PolicyVersion != input.PolicyVersion || record.IssuerSetID != input.IssuerSetID {
			return nil, [sha256.Size]byte{}, fmt.Errorf("record batch metadata mismatch")
		}
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Subject != records[j].Subject {
			return bytes.Compare([]byte(records[i].Subject), []byte(records[j].Subject)) < 0
		}
		return bytes.Compare([]byte(records[i].MsgTypeURL), []byte(records[j].MsgTypeURL)) < 0
	})

	protoRecords := make([]*authzpb.AuthorizationRecord, len(records))
	for i, record := range records {
		if i > 0 && record.Subject == records[i-1].Subject && record.MsgTypeURL == records[i-1].MsgTypeURL {
			return nil, [sha256.Size]byte{}, fmt.Errorf("duplicate authorization record")
		}
		protoRecords[i] = &authzpb.AuthorizationRecord{
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

	signDoc := &authzpb.AuthorizationBatchSignDoc{
		Domain:        input.Domain,
		ChainId:       input.ChainID,
		BatchId:       input.BatchID,
		PolicyId:      input.PolicyID,
		PolicyVersion: input.PolicyVersion,
		PolicyHash:    append([]byte(nil), input.PolicyHash...),
		IssuerSetId:   input.IssuerSetID,
		Records:       protoRecords,
	}
	signBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(signDoc)
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("marshal canonical sign document: %w", err)
	}
	return signBytes, sha256.Sum256(signBytes), nil
}

func validateCanonicalRecord(record AuthorizationRecord) error {
	if strings.TrimSpace(record.AuthorizationID) == "" {
		return fmt.Errorf("invalid authorization id")
	}
	if err := validateAccountAddress(record.Subject); err != nil {
		return fmt.Errorf("invalid subject: %w", err)
	}
	if record.MsgTypeURL != MsgSendTypeURL {
		return fmt.Errorf("unsupported message type")
	}
	if strings.TrimSpace(record.PolicyID) == "" || record.PolicyVersion == 0 || record.IssuerSetID == 0 {
		return fmt.Errorf("invalid record metadata")
	}
	if record.ValidFromHeight <= 0 || record.ValidUntilHeight < record.ValidFromHeight {
		return fmt.Errorf("invalid authorization height window")
	}
	if !denomPattern.MatchString(record.BankSendConstraints.Denom) {
		return fmt.Errorf("invalid denom")
	}
	if err := validateAccountAddress(record.BankSendConstraints.Receiver); err != nil {
		return fmt.Errorf("invalid receiver: %w", err)
	}
	if _, ok := canonicalAmount(record.BankSendConstraints.MaxAmount); !ok {
		return fmt.Errorf("invalid max amount")
	}
	return nil
}
