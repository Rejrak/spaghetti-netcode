package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestProtocolV121AlphaCompatibility(t *testing.T) {
	version, err := os.ReadFile("../../docs/authz/CONTRACT_VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(bytes.TrimSpace(version)); got != "authz-protocol-v1.2.1" {
		t.Fatalf("contract version: got %q", got)
	}

	fixture, input := loadCanonicalFixture(t)
	signDoc, err := BuildBatchSignDoc(trustedBatchContext(input), input.Records)
	if err != nil {
		t.Fatal(err)
	}
	if signDoc.Domain != "alpha.authzattrs.batch.v1" {
		t.Fatalf("batch domain: got %q", signDoc.Domain)
	}
	for i, want := range fixture.CanonicalRecordOrder {
		if signDoc.Records[i].Subject != want.Subject || signDoc.Records[i].MsgTypeURL != want.MsgTypeURL {
			t.Fatalf("canonical record %d mismatch", i)
		}
	}

	signers := []BatchSigner{
		goldenSigner(t, "issuer-beta", issuerBetaSeed),
		goldenSigner(t, "issuer-alpha", issuerAlphaSeed),
	}
	batch, batchHash, err := SignAuthorizationBatch(context.Background(), signDoc, signers)
	if err != nil {
		t.Fatal(err)
	}
	submitter := signDoc.Records[0].Subject
	msg, err := BuildBatchUpsertMessage(submitter, batch)
	if err != nil {
		t.Fatal(err)
	}

	if got := string(msg.Batch.SignDoc.ProtoReflect().Descriptor().FullName()); got != "alpha.authzattrs.v1.AuthorizationBatchSignDoc" {
		t.Fatalf("sign-doc full name: got %q", got)
	}
	if got := string(msg.Batch.ProtoReflect().Descriptor().FullName()); got != "alpha.authzattrs.v1.AuthorizationBatch" {
		t.Fatalf("batch full name: got %q", got)
	}
	if got := string(msg.ProtoReflect().Descriptor().FullName()); got != "alpha.authzattrs.v1.MsgBatchUpsertAuthorizations" {
		t.Fatalf("message full name: got %q", got)
	}
	if MsgBatchUpsertAuthorizationsTypeURL != "/alpha.authzattrs.v1.MsgBatchUpsertAuthorizations" {
		t.Fatalf("message type URL: got %q", MsgBatchUpsertAuthorizationsTypeURL)
	}

	signBytes, gotHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(signBytes); got != fixture.ExpectedSignBytesHex {
		t.Fatalf("sign bytes differ from Alpha fixture: got %s", got)
	}
	if batchHash != gotHash || gotHash != sha256.Sum256(signBytes) || hex.EncodeToString(gotHash[:]) != fixture.ExpectedBatchHashHex {
		t.Fatal("batch hash differs from Alpha fixture")
	}

	seeds := map[string]string{
		"issuer-alpha": issuerAlphaSeed,
		"issuer-beta":  issuerBetaSeed,
	}
	if len(msg.Batch.Signatures) != len(fixture.Issuers) {
		t.Fatalf("signature count: got %d want %d", len(msg.Batch.Signatures), len(fixture.Issuers))
	}
	for i, signature := range msg.Batch.Signatures {
		if i > 0 && bytes.Compare([]byte(msg.Batch.Signatures[i-1].IssuerId), []byte(signature.IssuerId)) >= 0 {
			t.Fatal("wire signatures are not in canonical issuer order")
		}
		seedHex, ok := seeds[signature.IssuerId]
		if !ok {
			t.Fatalf("unexpected issuer %q", signature.IssuerId)
		}
		seed, err := hex.DecodeString(seedHex)
		if err != nil {
			t.Fatal(err)
		}
		publicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		expectedPublicKey, expectedSignature := fixtureIssuer(t, fixture, signature.IssuerId)
		if !bytes.Equal(publicKey, expectedPublicKey) {
			t.Fatalf("public key for %s differs from Alpha fixture", signature.IssuerId)
		}
		if !bytes.Equal(signature.Signature, expectedSignature) {
			t.Fatalf("signature for %s differs from Alpha fixture", signature.IssuerId)
		}
		if !ed25519.Verify(publicKey, signBytes, signature.Signature) {
			t.Fatalf("signature for %s does not verify over sign bytes", signature.IssuerId)
		}
		if ed25519.Verify(publicKey, gotHash[:], signature.Signature) {
			t.Fatalf("signature for %s verifies over batch hash", signature.IssuerId)
		}
	}

	assertProtoSignDocMatchesLogical(t, msg.Batch.SignDoc, batch.SignDoc)
	if msg.Submitter != submitter {
		t.Fatalf("outer submitter: got %q want %q", msg.Submitter, submitter)
	}
	if msg.ProtoReflect().Descriptor().Fields().ByName("submitter") == nil {
		t.Fatal("outer message lacks submitter field")
	}
	for name, descriptor := range map[string]proto.Message{
		"sign doc":  msg.Batch.SignDoc,
		"record":    msg.Batch.SignDoc.Records[0],
		"signature": msg.Batch.Signatures[0],
	} {
		if descriptor.ProtoReflect().Descriptor().Fields().ByName("submitter") != nil {
			t.Fatalf("%s unexpectedly contains submitter", name)
		}
	}

	embeddedSignBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg.Batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embeddedSignBytes, signBytes) || hex.EncodeToString(embeddedSignBytes) != fixture.ExpectedSignBytesHex {
		t.Fatal("embedded sign doc changed canonical sign bytes")
	}
	outerBytes, err := MarshalBatchUpsertMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	repeatedOuterBytes, err := MarshalBatchUpsertMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(outerBytes, repeatedOuterBytes) {
		t.Fatal("outer message serialization is not deterministic")
	}

	reversedRecords := append([]AuthorizationRecord(nil), input.Records...)
	for left, right := 0, len(reversedRecords)-1; left < right; left, right = left+1, right-1 {
		reversedRecords[left], reversedRecords[right] = reversedRecords[right], reversedRecords[left]
	}
	reversedSignDoc, err := BuildBatchSignDoc(trustedBatchContext(input), reversedRecords)
	if err != nil {
		t.Fatal(err)
	}
	reversedBatch, reversedHash, err := SignAuthorizationBatch(context.Background(), reversedSignDoc, []BatchSigner{signers[1], signers[0]})
	if err != nil {
		t.Fatal(err)
	}
	reversedMsg, err := BuildBatchUpsertMessage(submitter, reversedBatch)
	if err != nil {
		t.Fatal(err)
	}
	reversedSignBytes, _, err := CanonicalBatchSignBytes(reversedBatch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if reversedHash != batchHash || !bytes.Equal(reversedSignBytes, signBytes) || !proto.Equal(reversedMsg.Batch, msg.Batch) {
		t.Fatal("reversed record/signer input changed canonical batch output")
	}
	for i, want := range fixture.CanonicalRecordOrder {
		if reversedMsg.Batch.SignDoc.Records[i].Subject != want.Subject || reversedMsg.Batch.SignDoc.Records[i].MsgTypeUrl != want.MsgTypeURL {
			t.Fatalf("reversed canonical record %d mismatch", i)
		}
	}
}
