package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	authzpb "spaghetti/internal/authorization/pb"

	"google.golang.org/protobuf/proto"
)

func goldenLogicalBatch(t *testing.T) (canonicalFixture, AuthorizationBatch) {
	t.Helper()
	fixture, input := loadCanonicalFixture(t)
	batch, _, err := SignAuthorizationBatch(context.Background(), input, []BatchSigner{
		goldenSigner(t, "issuer-beta", issuerBetaSeed),
		goldenSigner(t, "issuer-alpha", issuerAlphaSeed),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, batch
}

func cloneLogicalBatch(batch AuthorizationBatch) AuthorizationBatch {
	records := make([]AuthorizationRecord, len(batch.SignDoc.Records))
	for i, record := range batch.SignDoc.Records {
		records[i] = cloneAuthorizationRecord(record)
	}
	signatures := make([]BatchSignature, len(batch.Signatures))
	for i, signature := range batch.Signatures {
		signatures[i] = BatchSignature{
			IssuerID:  signature.IssuerID,
			Signature: append([]byte(nil), signature.Signature...),
		}
	}
	return AuthorizationBatch{
		SignDoc: BatchSignDoc{
			Domain:        batch.SignDoc.Domain,
			ChainID:       batch.SignDoc.ChainID,
			BatchID:       batch.SignDoc.BatchID,
			PolicyID:      batch.SignDoc.PolicyID,
			PolicyVersion: batch.SignDoc.PolicyVersion,
			PolicyHash:    append([]byte(nil), batch.SignDoc.PolicyHash...),
			IssuerSetID:   batch.SignDoc.IssuerSetID,
			Records:       records,
		},
		Signatures: signatures,
	}
}

func TestToProtoAuthorizationBatchPreservesGoldenSemantics(t *testing.T) {
	fixture, batch := goldenLogicalBatch(t)
	original := cloneLogicalBatch(batch)

	wireBatch, err := ToProtoAuthorizationBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch, original) {
		t.Fatal("conversion mutated caller batch")
	}
	assertProtoSignDocMatchesLogical(t, wireBatch.SignDoc, batch.SignDoc)
	if len(wireBatch.Signatures) != len(batch.Signatures) {
		t.Fatalf("signature count: got %d want %d", len(wireBatch.Signatures), len(batch.Signatures))
	}
	for i, signature := range wireBatch.Signatures {
		if signature.IssuerId != batch.Signatures[i].IssuerID || !bytes.Equal(signature.Signature, batch.Signatures[i].Signature) {
			t.Fatalf("signature %d mismatch", i)
		}
		_, expected := fixtureIssuer(t, fixture, signature.IssuerId)
		if !bytes.Equal(signature.Signature, expected) {
			t.Fatalf("signature %d differs from golden fixture", i)
		}
	}
	if wireBatch.Signatures[0].IssuerId != "issuer-alpha" || wireBatch.Signatures[1].IssuerId != "issuer-beta" {
		t.Fatalf("noncanonical signature order: %#v", wireBatch.Signatures)
	}

	reversed := cloneLogicalBatch(batch)
	reversed.Signatures[0], reversed.Signatures[1] = reversed.Signatures[1], reversed.Signatures[0]
	reversedOriginal := cloneLogicalBatch(reversed)
	reversedWire, err := ToProtoAuthorizationBatch(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reversed, reversedOriginal) {
		t.Fatal("conversion mutated reversed caller batch")
	}
	if !proto.Equal(wireBatch, reversedWire) {
		t.Fatal("caller signature order changed protobuf output")
	}

	before, batchHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	after, err := (proto.MarshalOptions{Deterministic: true}).Marshal(wireBatch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || hex.EncodeToString(after) != fixture.ExpectedSignBytesHex {
		t.Fatal("wire conversion changed canonical issuer sign bytes")
	}
	if batchHash != sha256.Sum256(after) || hex.EncodeToString(batchHash[:]) != fixture.ExpectedBatchHashHex {
		t.Fatal("wire conversion changed golden batch hash")
	}

	wantHashByte := wireBatch.SignDoc.PolicyHash[0]
	wantSignatureByte := wireBatch.Signatures[0].Signature[0]
	batch.SignDoc.PolicyHash[0] ^= 0xff
	batch.Signatures[0].Signature[0] ^= 0xff
	if wireBatch.SignDoc.PolicyHash[0] != wantHashByte || wireBatch.Signatures[0].Signature[0] != wantSignatureByte {
		t.Fatal("protobuf output aliases caller policy hash or signature")
	}
}

func TestToProtoAuthorizationBatchRejectsInvalidSignatures(t *testing.T) {
	_, valid := goldenLogicalBatch(t)
	tests := []struct {
		name   string
		mutate func(*AuthorizationBatch)
	}{
		{name: "empty list", mutate: func(batch *AuthorizationBatch) { batch.Signatures = nil }},
		{name: "empty issuer id", mutate: func(batch *AuthorizationBatch) { batch.Signatures[0].IssuerID = "" }},
		{name: "63 byte signature", mutate: func(batch *AuthorizationBatch) { batch.Signatures[0].Signature = make([]byte, 63) }},
		{name: "duplicate issuer id", mutate: func(batch *AuthorizationBatch) { batch.Signatures[1].IssuerID = batch.Signatures[0].IssuerID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := cloneLogicalBatch(valid)
			tt.mutate(&batch)
			if _, err := ToProtoAuthorizationBatch(batch); err == nil {
				t.Fatal("expected signature validation error")
			}
		})
	}
}

func TestBuildAndMarshalBatchUpsertMessage(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	submitter := batch.SignDoc.Records[0].Subject
	wireBatch, err := ToProtoAuthorizationBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := BuildBatchUpsertMessage(submitter, batch)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(msg.ProtoReflect().Descriptor().FullName()); got != "alpha.authzattrs.v1.MsgBatchUpsertAuthorizations" {
		t.Fatalf("full message name: got %q", got)
	}
	if MsgBatchUpsertAuthorizationsTypeURL != "/alpha.authzattrs.v1.MsgBatchUpsertAuthorizations" {
		t.Fatalf("type URL: got %q", MsgBatchUpsertAuthorizationsTypeURL)
	}
	if msg.Submitter != submitter || !proto.Equal(msg.Batch, wireBatch) {
		t.Fatal("submitter changed embedded signed batch")
	}
	if msg.Batch.SignDoc == nil || !proto.Equal(msg.Batch.SignDoc, wireBatch.SignDoc) {
		t.Fatal("submitter was inserted into or changed the sign document")
	}

	first, err := MarshalBatchUpsertMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalBatchUpsertMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("outer message marshal is not deterministic")
	}
	if _, err := BuildBatchUpsertMessage("not-an-address", batch); err == nil {
		t.Fatal("expected invalid submitter error")
	}
	if _, err := MarshalBatchUpsertMessage(nil); err == nil {
		t.Fatal("expected nil message error")
	}
}

func assertProtoSignDocMatchesLogical(t *testing.T, got *authzpb.AuthorizationBatchSignDoc, want BatchSignDoc) {
	t.Helper()
	if got.Domain != want.Domain || got.ChainId != want.ChainID || got.BatchId != want.BatchID || got.PolicyId != want.PolicyID || got.PolicyVersion != want.PolicyVersion || !bytes.Equal(got.PolicyHash, want.PolicyHash) || got.IssuerSetId != want.IssuerSetID {
		t.Fatal("sign document scalar mismatch")
	}
	if len(got.Records) != len(want.Records) {
		t.Fatalf("record count: got %d want %d", len(got.Records), len(want.Records))
	}
	for i, record := range got.Records {
		expected := want.Records[i]
		if record.AuthorizationId != expected.AuthorizationID || record.Subject != expected.Subject || record.MsgTypeUrl != expected.MsgTypeURL || record.PolicyId != expected.PolicyID || record.PolicyVersion != expected.PolicyVersion || record.IssuerSetId != expected.IssuerSetID || record.ValidFromHeight != expected.ValidFromHeight || record.ValidUntilHeight != expected.ValidUntilHeight || record.Revoked != expected.Revoked {
			t.Fatalf("record %d scalar mismatch", i)
		}
		constraints := record.BankSendConstraints
		if constraints == nil || constraints.Denom != expected.BankSendConstraints.Denom || constraints.Receiver != expected.BankSendConstraints.Receiver || constraints.MaxAmount != expected.BankSendConstraints.MaxAmount {
			t.Fatalf("record %d constraints mismatch", i)
		}
	}
}
