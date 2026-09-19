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

// CanonicalizeBatchSignDoc validates, sorts, and detaches a Protocol V1.2.1
// sign document without retaining or mutating caller-owned values.
func CanonicalizeBatchSignDoc(input BatchSignDoc) (BatchSignDoc, error) {
	if input.Domain != BatchDomain {
		return BatchSignDoc{}, fmt.Errorf("invalid batch domain")
	}
	if input.ChainID == "" || input.BatchID == 0 || strings.TrimSpace(input.PolicyID) == "" || input.PolicyVersion == 0 || len(input.PolicyHash) != sha256.Size || input.IssuerSetID == 0 || len(input.Records) == 0 {
		return BatchSignDoc{}, fmt.Errorf("invalid batch sign document")
	}

	records := make([]AuthorizationRecord, len(input.Records))
	for i, record := range input.Records {
		if err := validateCanonicalRecord(record); err != nil {
			return BatchSignDoc{}, err
		}
		if record.PolicyID != input.PolicyID || record.PolicyVersion != input.PolicyVersion || record.IssuerSetID != input.IssuerSetID {
			return BatchSignDoc{}, fmt.Errorf("record batch metadata mismatch")
		}
		records[i] = cloneAuthorizationRecord(record)
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Subject != records[j].Subject {
			return bytes.Compare([]byte(records[i].Subject), []byte(records[j].Subject)) < 0
		}
		return bytes.Compare([]byte(records[i].MsgTypeURL), []byte(records[j].MsgTypeURL)) < 0
	})

	for i, record := range records {
		if i > 0 && record.Subject == records[i-1].Subject && record.MsgTypeURL == records[i-1].MsgTypeURL {
			return BatchSignDoc{}, fmt.Errorf("duplicate authorization record")
		}
	}
	return BatchSignDoc{
		Domain:        input.Domain,
		ChainID:       input.ChainID,
		BatchID:       input.BatchID,
		PolicyID:      input.PolicyID,
		PolicyVersion: input.PolicyVersion,
		PolicyHash:    append([]byte(nil), input.PolicyHash...),
		IssuerSetID:   input.IssuerSetID,
		Records:       records,
	}, nil
}

// CanonicalBatchSignBytes serializes only a newly canonicalized sign document.
func CanonicalBatchSignBytes(input BatchSignDoc) ([]byte, [sha256.Size]byte, error) {
	canonical, err := CanonicalizeBatchSignDoc(input)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	protoRecords := make([]*authzpb.AuthorizationRecord, len(canonical.Records))
	for i, record := range canonical.Records {
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
		Domain:        canonical.Domain,
		ChainId:       canonical.ChainID,
		BatchId:       canonical.BatchID,
		PolicyId:      canonical.PolicyID,
		PolicyVersion: canonical.PolicyVersion,
		PolicyHash:    append([]byte(nil), canonical.PolicyHash...),
		IssuerSetId:   canonical.IssuerSetID,
		Records:       protoRecords,
	}
	signBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(signDoc)
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("marshal canonical sign document: %w", err)
	}
	return signBytes, sha256.Sum256(signBytes), nil
}

func cloneAuthorizationRecord(record AuthorizationRecord) AuthorizationRecord {
	return AuthorizationRecord{
		AuthorizationID:  record.AuthorizationID,
		Subject:          record.Subject,
		MsgTypeURL:       record.MsgTypeURL,
		PolicyID:         record.PolicyID,
		PolicyVersion:    record.PolicyVersion,
		IssuerSetID:      record.IssuerSetID,
		ValidFromHeight:  record.ValidFromHeight,
		ValidUntilHeight: record.ValidUntilHeight,
		Revoked:          record.Revoked,
		BankSendConstraints: BankSendConstraints{
			Denom:     record.BankSendConstraints.Denom,
			Receiver:  record.BankSendConstraints.Receiver,
			MaxAmount: record.BankSendConstraints.MaxAmount,
		},
	}
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
