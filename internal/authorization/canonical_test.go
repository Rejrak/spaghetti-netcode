package authorization

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	authzpb "spaghetti/internal/authorization/pb"

	"google.golang.org/protobuf/proto"
)

type canonicalFixture struct {
	Domain               string                `json:"domain"`
	ChainID              string                `json:"chain_id"`
	BatchID              uint64                `json:"batch_id"`
	PolicyID             string                `json:"policy_id"`
	PolicyVersion        uint64                `json:"policy_version"`
	PolicyHashHex        string                `json:"policy_hash_hex"`
	IssuerSetID          uint64                `json:"issuer_set_id"`
	Records              []AuthorizationRecord `json:"records"`
	CanonicalRecordOrder []struct {
		Subject    string `json:"subject"`
		MsgTypeURL string `json:"msg_type_url"`
	} `json:"canonical_record_order"`
	ExpectedSignBytesHex string `json:"expected_sign_bytes_hex"`
	ExpectedBatchHashHex string `json:"expected_batch_hash_hex"`
}

func loadCanonicalFixture(t *testing.T) (canonicalFixture, BatchSignDoc) {
	t.Helper()
	data, err := os.ReadFile("../../docs/authz/testdata/v1.2/canonical-batch-sign-doc.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture canonicalFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	policyHash, err := hex.DecodeString(fixture.PolicyHashHex)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, BatchSignDoc{
		Domain:        fixture.Domain,
		ChainID:       fixture.ChainID,
		BatchID:       fixture.BatchID,
		PolicyID:      fixture.PolicyID,
		PolicyVersion: fixture.PolicyVersion,
		PolicyHash:    policyHash,
		IssuerSetID:   fixture.IssuerSetID,
		Records:       fixture.Records,
	}
}

func TestCanonicalBatchSignBytesMatchesAlphaFixture(t *testing.T) {
	fixture, input := loadCanonicalFixture(t)
	originalHash := append([]byte(nil), input.PolicyHash...)
	originalRecords := append([]AuthorizationRecord(nil), input.Records...)

	signBytes, batchHash, err := CanonicalBatchSignBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(signBytes); got != fixture.ExpectedSignBytesHex {
		t.Fatalf("sign bytes mismatch\n got: %s\nwant: %s", got, fixture.ExpectedSignBytesHex)
	}
	if got := hex.EncodeToString(batchHash[:]); got != fixture.ExpectedBatchHashHex {
		t.Fatalf("batch hash mismatch: got %s want %s", got, fixture.ExpectedBatchHashHex)
	}
	if !bytes.Equal(input.PolicyHash, originalHash) || !reflect.DeepEqual(input.Records, originalRecords) {
		t.Fatal("canonicalization mutated caller input")
	}

	var decoded authzpb.AuthorizationBatchSignDoc
	if err := proto.Unmarshal(signBytes, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Records) != len(fixture.CanonicalRecordOrder) {
		t.Fatalf("record count: got %d want %d", len(decoded.Records), len(fixture.CanonicalRecordOrder))
	}
	for i, want := range fixture.CanonicalRecordOrder {
		if decoded.Records[i].Subject != want.Subject || decoded.Records[i].MsgTypeUrl != want.MsgTypeURL {
			t.Fatalf("record %d order mismatch: got (%s,%s)", i, decoded.Records[i].Subject, decoded.Records[i].MsgTypeUrl)
		}
	}

	againBytes, againHash, err := CanonicalBatchSignBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signBytes, againBytes) || batchHash != againHash {
		t.Fatal("repeated canonicalization was not deterministic")
	}
}

func TestCanonicalBatchSignBytesRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BatchSignDoc)
	}{
		{
			name: "duplicate logical key",
			mutate: func(input *BatchSignDoc) {
				duplicate := input.Records[0]
				duplicate.AuthorizationID = "different-audit-id"
				input.Records = append(input.Records, duplicate)
			},
		},
		{
			name: "noncanonical record",
			mutate: func(input *BatchSignDoc) {
				input.Records[0].BankSendConstraints.MaxAmount = "05000"
			},
		},
		{
			name: "policy mismatch",
			mutate: func(input *BatchSignDoc) {
				input.Records[0].PolicyID = "other-policy"
			},
		},
		{
			name: "version mismatch",
			mutate: func(input *BatchSignDoc) {
				input.Records[0].PolicyVersion++
			},
		},
		{
			name: "issuer set mismatch",
			mutate: func(input *BatchSignDoc) {
				input.Records[0].IssuerSetID++
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, input := loadCanonicalFixture(t)
			tt.mutate(&input)
			if _, _, err := CanonicalBatchSignBytes(input); err == nil {
				t.Fatal("expected canonicalization error")
			}
		})
	}
}
