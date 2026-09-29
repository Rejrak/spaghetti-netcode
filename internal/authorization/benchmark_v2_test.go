package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
)

var benchmarkV2Bytes []byte
var benchmarkV2Certificate AuthorizationCertificateV2

func BenchmarkV1CanonicalBatchSignBytes(b *testing.B) {
	_, doc := loadCanonicalFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encoded, _, err := CanonicalBatchSignBytes(doc)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkV2Bytes = encoded
	}
}

func BenchmarkV2CanonicalSignBytes(b *testing.B) {
	intent, trusted := goldenInputsV2()
	doc, err := BuildCertificateSignDocV2(intent, trusted)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encoded, _, err := CanonicalCertificateSignBytesV2(doc)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkV2Bytes = encoded
	}
}

func BenchmarkV2CertificateBuildAndSign(b *testing.B) {
	intent, trusted := goldenInputsV2()
	signers := make([]CertificateSignerV2, 2)
	for i, id := range []string{"issuer-alpha", "issuer-beta"} {
		seed := bytes.Repeat([]byte{byte(i + 1)}, ed25519.SeedSize) // TEST-ONLY
		signer, err := NewEd25519BatchSigner(id, ed25519.NewKeyFromSeed(seed))
		if err != nil {
			b.Fatal(err)
		}
		signers[i] = signer
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		certificate, _, _, err := BuildAuthorizationCertificateV2(context.Background(), intent, trusted, signers)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkV2Certificate = certificate
	}
}

func BenchmarkV2IssuerEd25519Sign(b *testing.B) {
	intent, trusted := goldenInputsV2()
	doc, err := BuildCertificateSignDocV2(intent, trusted)
	if err != nil {
		b.Fatal(err)
	}
	signBytes, _, err := CanonicalCertificateSignBytesV2(doc)
	if err != nil {
		b.Fatal(err)
	}
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize) // TEST-ONLY
	signer, err := NewEd25519BatchSigner("issuer-alpha", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		signature, err := signer.Sign(context.Background(), signBytes)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkV2Bytes = signature
	}
}

func BenchmarkV2NativeTransactionBuildSign(b *testing.B) {
	issued, signer := signedV2Fixture(b)
	config, err := NewCosmosTxConfigV2()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		signed, err := BuildSignedV2Transaction(context.Background(), config, issued, signer)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkV2Bytes = signed.TxBytes
	}
}
