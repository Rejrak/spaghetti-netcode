package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	v2pb "spaghetti/internal/authorization/pb/v2"

	"google.golang.org/protobuf/proto"
)

const goldenSignBytesV2 = "0a1f616c7068612e617574687a61747472732e63657274696669636174652e76321297010a07616c7068612d31122d636f736d6f733164757a70786b753561746d3938716b3679777667646a7a6e3530797a763930713763337234341a2d636f736d6f7331737365766e646c6739393761383977707732777639786a32766168687163797430676d6c38342205746f6b656e2a04313030303007380340734a09763220676f6c64656e520c0a057374616b65120331303058c09a0c1a10706f6c6963792d62616e6b2d73656e6420022a205ad8c4fa036c6238f322b3bfc2c012f0f12d9c6af391ba9d26acfe08a1d01d1330093864406e"
const goldenDigestV2 = "23d2bdf82cc29dafa3ff0ff8a42e74dc1b88864632a69cca8f9d7055387e844b"

type fixtureSignerV2 struct {
	id        string
	signature []byte
	err       error
	message   []byte
}

func (s *fixtureSignerV2) IssuerID() string { return s.id }
func (s *fixtureSignerV2) Sign(_ context.Context, message []byte) ([]byte, error) {
	s.message = append([]byte(nil), message...)
	return s.signature, s.err
}

func goldenInputsV2() (AuthorizationIntentV2, TrustedCertificateContextV2) {
	return AuthorizationIntentV2{
			Subject:  "cosmos1duzpxku5atm98qk6ywvgdjzn50yzv90q7c3r44",
			Receiver: "cosmos1ssevndlg997a89wpw2wv9xj2vahhqcyt0gml84",
			Denom:    "token", Amount: "1000", AccountNumber: 7, Sequence: 3,
			TimeoutHeight: 115, Memo: "v2 golden", FeeAmount: []FeeCoinV2{{Denom: "stake", Amount: "100"}},
			GasLimit: 200000,
		}, TrustedCertificateContextV2{
			ChainID: "alpha-1", PolicyID: "policy-bank-send", PolicyVersion: 2,
			PolicyHash: hashBytesV2("test-only-policy-v2"), IssuerSetID: 9,
			ValidFromHeight: 100, ValidUntilHeight: 110,
		}
}

func hashBytesV2(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func decodeHexV2(t *testing.T, value string) []byte {
	t.Helper()
	b, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestV2GoldenVector(t *testing.T) {
	intent, trusted := goldenInputsV2()
	doc, err := BuildCertificateSignDocV2(intent, trusted)
	if err != nil {
		t.Fatal(err)
	}
	signBytes, digest, err := CanonicalCertificateSignBytesV2(doc)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(signBytes) != goldenSignBytesV2 {
		t.Fatalf("golden sign bytes mismatch: %x", signBytes)
	}
	if hex.EncodeToString(digest[:]) != goldenDigestV2 {
		t.Fatalf("golden digest mismatch: %x", digest)
	}
	fixtures := []struct{ id, publicKey, signature string }{
		{"issuer-alpha", "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8", "b1bea693676fc45cbc80c3cf2127166238d3289466166ab3630a684e6f7bb1d800601459bb212a98d2d0b8c1f5a96e359744b1e1ce8a0430298d61dd334d6b0d"},
		{"issuer-beta", "29acbae141bccaf0b22e1a94d34d0bc7361e526d0bfe12c89794bc9322966dd7", "02d446a06af1329d2dba3e752a6f0689d9625c8e46a6f55b2ee4809becf1c1e133d6ad7b0196f9b15df653c97679a81250912525f88df0c39a99b4fa3efc4008"},
	}
	for _, f := range fixtures {
		pk, sig := decodeHexV2(t, f.publicKey), decodeHexV2(t, f.signature)
		if len(pk) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pk, signBytes, sig) {
			t.Fatalf("%s signature invalid over sign bytes", f.id)
		}
		if ed25519.Verify(pk, digest[:], sig) {
			t.Fatalf("%s signature verified over digest", f.id)
		}
	}
	beta := &fixtureSignerV2{id: fixtures[1].id, signature: decodeHexV2(t, fixtures[1].signature)}
	alpha := &fixtureSignerV2{id: fixtures[0].id, signature: decodeHexV2(t, fixtures[0].signature)}
	cert, builtBytes, builtDigest, err := BuildAuthorizationCertificateV2(context.Background(), intent, trusted, []CertificateSignerV2{beta, alpha})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(builtBytes, signBytes) || builtDigest != digest || !bytes.Equal(alpha.message, signBytes) || !bytes.Equal(beta.message, signBytes) {
		t.Fatal("signer did not receive exact canonical sign bytes")
	}
	if cert.Signatures[0].IssuerID != "issuer-alpha" || cert.Signatures[1].IssuerID != "issuer-beta" {
		t.Fatal("noncanonical signature order")
	}
	encoded, err := MarshalAuthorizationCertificateV2(cert)
	if err != nil {
		t.Fatal(err)
	}
	var wire v2pb.AuthorizationCertificateV2
	if err := proto.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire.ProtoReflect().Descriptor().FullName()) != "alpha.authzattrs.v2.AuthorizationCertificateV2" || CertificateTypeURLV2 != "/alpha.authzattrs.v2.AuthorizationCertificateV2" {
		t.Fatal("wrong V2 wire identity")
	}
	if !proto.Equal(&wire, mustProtoCertificateV2(t, cert)) {
		t.Fatal("protobuf round-trip mismatch")
	}
	originalHash := append([]byte(nil), wire.SignDoc.PolicyHash...)
	originalSignature := append([]byte(nil), wire.Signatures[0].Signature...)
	cert.SignDoc.PolicyHash[0] ^= 1
	cert.Signatures[0].Signature[0] ^= 1
	if !bytes.Equal(wire.SignDoc.PolicyHash, originalHash) || !bytes.Equal(wire.Signatures[0].Signature, originalSignature) {
		t.Fatal("protobuf retained caller-owned bytes")
	}
}

func mustProtoCertificateV2(t *testing.T, cert AuthorizationCertificateV2) *v2pb.AuthorizationCertificateV2 {
	t.Helper()
	w, err := ToProtoAuthorizationCertificateV2(cert)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestV2CanonicalDetachedAndFeeSorting(t *testing.T) {
	intent, trusted := goldenInputsV2()
	intent.FeeAmount = []FeeCoinV2{{"ztoken", "0"}, {"stake", "100"}}
	originalFees := append([]FeeCoinV2(nil), intent.FeeAmount...)
	originalHash := append([]byte(nil), trusted.PolicyHash...)
	doc, err := BuildCertificateSignDocV2(intent, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Intent.FeeAmount[0].Denom != "stake" || !reflect.DeepEqual(intent.FeeAmount, originalFees) {
		t.Fatal("fee sort/mutation")
	}
	first, firstHash, err := CanonicalCertificateSignBytesV2(doc)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := CanonicalCertificateSignBytesV2(doc)
	if err != nil || !bytes.Equal(first, second) || firstHash != secondHash {
		t.Fatal("nondeterministic sign bytes")
	}
	intent.FeeAmount[0].Amount = "999"
	trusted.PolicyHash[0] ^= 1
	if doc.Intent.FeeAmount[1].Amount != "0" || !bytes.Equal(doc.PolicyHash, originalHash) {
		t.Fatal("caller alias retained")
	}
}

func TestV2InvalidSignDocuments(t *testing.T) {
	baseIntent, baseTrusted := goldenInputsV2()
	cases := []struct {
		name   string
		change func(*AuthorizationIntentV2, *TrustedCertificateContextV2)
	}{
		{"chain mismatch", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.ChainID = "wrong" }},
		{"subject", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.Subject = "bad" }},
		{"receiver", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.Receiver = "bad" }},
		{"denom", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.Denom = "x" }},
		{"amount", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.Amount = "01" }},
		{"fee amount", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.FeeAmount[0].Amount = "00" }},
		{"fee negative", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.FeeAmount[0].Amount = "-1" }},
		{"duplicate fee", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) {
			i.FeeAmount = append(i.FeeAmount, i.FeeAmount[0])
		}},
		{"five fees", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) {
			i.FeeAmount = []FeeCoinV2{{"aaa", "0"}, {"bbb", "0"}, {"ccc", "0"}, {"ddd", "0"}, {"eee", "0"}}
		}},
		{"hash", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.PolicyHash = c.PolicyHash[:31] }},
		{"policy", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.PolicyID = "" }},
		{"version", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.PolicyVersion = 0 }},
		{"issuer set", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.IssuerSetID = 0 }},
		{"valid from", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.ValidFromHeight = 0 }},
		{"valid until", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.ValidUntilHeight = 99 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent, trusted := baseIntent, baseTrusted
			intent.FeeAmount = append([]FeeCoinV2(nil), baseIntent.FeeAmount...)
			trusted.PolicyHash = append([]byte(nil), baseTrusted.PolicyHash...)
			tc.change(&intent, &trusted)
			if _, err := BuildCertificateSignDocV2(intent, trusted); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

func TestV2CanonicalSignDocParityWithAlpha(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*AuthorizationIntentV2, *TrustedCertificateContextV2)
	}{
		{"zero gas", func(i *AuthorizationIntentV2, _ *TrustedCertificateContextV2) { i.GasLimit = 0 }},
		{"nonempty whitespace chain ID", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.ChainID = " " }},
		{"nonempty whitespace policy ID", func(_ *AuthorizationIntentV2, c *TrustedCertificateContextV2) { c.PolicyID = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent, trusted := goldenInputsV2()
			tc.change(&intent, &trusted)
			doc, err := BuildCertificateSignDocV2(intent, trusted)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Intent.GasLimit != intent.GasLimit || doc.Intent.ChainID != trusted.ChainID || doc.PolicyID != trusted.PolicyID {
				t.Fatal("canonical sign doc changed a defined field")
			}
			if _, _, err := CanonicalCertificateSignBytesV2(doc); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestV2SignerValidation(t *testing.T) {
	intent, trusted := goldenInputsV2()
	doc, err := BuildCertificateSignDocV2(intent, trusted)
	if err != nil {
		t.Fatal(err)
	}
	ok := &fixtureSignerV2{id: "a", signature: make([]byte, ed25519.SignatureSize)}
	cases := []struct {
		name    string
		signers []CertificateSignerV2
	}{
		{"zero", nil}, {"nil", []CertificateSignerV2{nil}},
		{"typed nil", []CertificateSignerV2{(*fixtureSignerV2)(nil)}},
		{"empty id", []CertificateSignerV2{&fixtureSignerV2{id: " ", signature: make([]byte, 64)}}},
		{"duplicate", []CertificateSignerV2{ok, ok}},
		{"bad length", []CertificateSignerV2{&fixtureSignerV2{id: "a", signature: make([]byte, 63)}}},
		{"error", []CertificateSignerV2{&fixtureSignerV2{id: "a", err: errors.New("sign failed")}}},
	}
	tooMany := make([]CertificateSignerV2, 17)
	for i := range tooMany {
		tooMany[i] = ok
	}
	cases = append(cases, struct {
		name    string
		signers []CertificateSignerV2
	}{"seventeen", tooMany})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cert, signed, _, err := SignAuthorizationCertificateV2(context.Background(), doc, tc.signers)
			if err == nil || len(cert.Signatures) != 0 || signed != nil {
				t.Fatal("invalid signer returned certificate")
			}
		})
	}
	if _, _, _, err := SignAuthorizationCertificateV2(nil, doc, []CertificateSignerV2{ok}); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestV2SoftwareEd25519SignsDirectBytes(t *testing.T) {
	intent, trusted := goldenInputsV2()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	private := ed25519.NewKeyFromSeed(seed)
	signer, err := NewEd25519BatchSigner("test-only", private)
	if err != nil {
		t.Fatal(err)
	}
	cert, signBytes, digest, err := BuildAuthorizationCertificateV2(context.Background(), intent, trusted, []CertificateSignerV2{signer})
	if err != nil {
		t.Fatal(err)
	}
	pk := private.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pk, signBytes, cert.Signatures[0].Signature) || ed25519.Verify(pk, digest[:], cert.Signatures[0].Signature) {
		t.Fatal("signature did not cover direct bytes")
	}
	encoded, err := MarshalAuthorizationCertificateV2(cert)
	if err != nil || len(encoded) == 0 || len(encoded) > 4096 {
		t.Fatal("invalid wire certificate")
	}
	if !strings.Contains(string(cert.Signatures[0].IssuerID), "test-only") {
		t.Fatal("issuer ID lost")
	}
}

func TestV2CertificateSizeLimit(t *testing.T) {
	intent, trusted := goldenInputsV2()
	intent.Memo = strings.Repeat("x", 4096)
	signer := &fixtureSignerV2{id: "issuer-alpha", signature: make([]byte, ed25519.SignatureSize)}
	if _, _, _, err := BuildAuthorizationCertificateV2(context.Background(), intent, trusted, []CertificateSignerV2{signer}); err == nil {
		t.Fatal("oversized certificate accepted")
	}
}
