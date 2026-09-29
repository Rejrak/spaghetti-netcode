package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/protobuf/proto"
	v2pb "spaghetti/internal/authorization/pb/v2"
)

func signedV2Fixture(t testing.TB) (CertificateIssueResultV2, CosmosAccountSignerV2) {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	keys := keyring.NewInMemory(codec.NewProtoCodec(registry))
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about" // TEST-ONLY
	record, err := keys.NewAccount("alice", mnemonic, "", sdk.FullFundraiserPath, hd.Secp256k1)
	if err != nil {
		t.Fatal(err)
	}
	address, err := record.GetAddress()
	if err != nil {
		t.Fatal(err)
	}
	intent, trusted := goldenInputsV2()
	intent.Subject = address.String()
	issuer := &fixtureSignerV2{id: "issuer-alpha", signature: bytes.Repeat([]byte{7}, ed25519.SignatureSize)}
	certificate, _, digest, err := BuildAuthorizationCertificateV2(context.Background(), intent, trusted, []CertificateSignerV2{issuer})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := MarshalAuthorizationCertificateV2(certificate)
	if err != nil {
		t.Fatal(err)
	}
	return CertificateIssueResultV2{
		Intent: certificate.SignDoc.Intent, Certificate: certificate, CertificateBytes: encoded,
		Digest: digest, AccountState: AlphaAccountStateV2{ChainID: certificate.SignDoc.Intent.ChainID, AccountNumber: intent.AccountNumber, Sequence: intent.Sequence, CurrentHeight: 100},
	}, CosmosAccountSignerV2{Keyring: keys, Name: "alice"}
}

func TestBuildSignedV2Transaction(t *testing.T) {
	result, signer := signedV2Fixture(t)
	config, err := NewCosmosTxConfigV2()
	if err != nil {
		t.Fatal(err)
	}
	got, err := BuildSignedV2Transaction(context.Background(), config, result, signer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TxBytes) == 0 || got.Subject != result.Intent.Subject || got.Sequence != result.Intent.Sequence || got.CertificateDigest != result.Digest {
		t.Fatal("incorrect signed transaction result")
	}
	tx := got.Tx.(interface {
		sdk.Tx
		GetExtensionOptions() []*codectypes.Any
		GetNonCriticalExtensionOptions() []*codectypes.Any
		GetMemo() string
		GetTimeoutHeight() uint64
		GetFee() sdk.Coins
		GetGas() uint64
		GetSignaturesV2() ([]signing.SignatureV2, error)
	})
	msg, ok := tx.GetMsgs()[0].(*banktypes.MsgSend)
	if !ok || len(tx.GetMsgs()) != 1 || msg.FromAddress != result.Intent.Subject || msg.ToAddress != result.Intent.Receiver ||
		len(msg.Amount) != 1 || msg.Amount[0].Denom != result.Intent.Denom || msg.Amount[0].Amount.String() != result.Intent.Amount {
		t.Fatal("wrong direct MsgSend")
	}
	if len(tx.GetExtensionOptions()) != 1 || tx.GetExtensionOptions()[0].TypeUrl != CertificateTypeURLV2 ||
		!bytes.Equal(tx.GetExtensionOptions()[0].Value, result.CertificateBytes) || len(tx.GetNonCriticalExtensionOptions()) != 0 {
		t.Fatal("wrong critical certificate extension")
	}
	var cert v2pb.AuthorizationCertificateV2
	if err := proto.Unmarshal(tx.GetExtensionOptions()[0].Value, &cert); err != nil || cert.SignDoc == nil || cert.SignDoc.Intent == nil || cert.SignDoc.Intent.Subject != result.Intent.Subject {
		t.Fatal("certificate did not survive protobuf round trip")
	}
	if tx.GetMemo() != result.Intent.Memo || tx.GetTimeoutHeight() != result.Intent.TimeoutHeight || tx.GetGas() != result.Intent.GasLimit ||
		len(tx.GetFee()) != 1 || tx.GetFee()[0].Denom != result.Intent.FeeAmount[0].Denom || tx.GetFee()[0].Amount.String() != result.Intent.FeeAmount[0].Amount {
		t.Fatal("fee, memo, timeout or gas mismatch")
	}
	sigs, err := tx.GetSignaturesV2()
	if err != nil || len(sigs) != 1 || sigs[0].Sequence != result.Intent.Sequence {
		t.Fatal("wrong signer info")
	}
	if data, ok := sigs[0].Data.(*signing.SingleSignatureData); !ok || data.SignMode != signing.SignMode_SIGN_MODE_DIRECT {
		t.Fatal("not signed in direct mode")
	}
	assertSDKSignature(t, config, got.Tx, result, true)
	wrongAccountNumber := result
	wrongAccountNumber.Intent.AccountNumber++
	assertSDKSignature(t, config, got.Tx, wrongAccountNumber, false)
	// SDK SIGN_MODE_DIRECT covers the final body, including certificate and MsgSend.
	for _, change := range []struct {
		name   string
		mutate func(tx sdk.Tx) error
	}{
		{"certificate", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			b.(client.ExtendedTxBuilder).SetExtensionOptions(&codectypes.Any{TypeUrl: CertificateTypeURLV2, Value: append(append([]byte(nil), result.CertificateBytes...), 1)})
			return nil
		}},
		{"MsgSend", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			return b.SetMsgs(&banktypes.MsgSend{FromAddress: result.Intent.Subject, ToAddress: testReceiver, Amount: sdk.NewCoins(sdk.NewInt64Coin("token", 1001))})
		}},
		{"memo", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			b.SetMemo("changed")
			return nil
		}},
		{"timeout", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			b.SetTimeoutHeight(result.Intent.TimeoutHeight + 1)
			return nil
		}},
		{"fee", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			b.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin("stake", 101)))
			return nil
		}},
		{"gas", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			b.SetGasLimit(result.Intent.GasLimit + 1)
			return nil
		}},
		{"sequence", func(tx sdk.Tx) error {
			b, e := config.WrapTxBuilder(tx)
			if e != nil {
				return e
			}
			s, e := tx.(interface {
				GetSignaturesV2() ([]signing.SignatureV2, error)
			}).GetSignaturesV2()
			if e != nil {
				return e
			}
			s[0].Sequence++
			return b.SetSignatures(s...)
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			decoded, err := config.TxDecoder()(got.TxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if err := change.mutate(decoded); err != nil {
				t.Fatal(err)
			}
			assertSDKSignature(t, config, decoded, result, false)
		})
	}
}

func TestBuildSignedV2TransactionRejectsWrongSignerAndMetadata(t *testing.T) {
	result, signer := signedV2Fixture(t)
	config, err := NewCosmosTxConfigV2()
	if err != nil {
		t.Fatal(err)
	}
	wrong := result
	wrong.Intent.Subject = testSubject
	if _, err := BuildSignedV2Transaction(context.Background(), config, wrong, signer); err == nil {
		t.Fatal("accepted mismatched intent")
	}
	wrong = result
	wrong.AccountState.Sequence++
	if _, err := BuildSignedV2Transaction(context.Background(), config, wrong, signer); err == nil {
		t.Fatal("silently adjusted sequence")
	}
	wrong = result
	wrong.CertificateBytes = nil
	if _, err := BuildSignedV2Transaction(context.Background(), config, wrong, signer); err == nil {
		t.Fatal("accepted missing certificate bytes")
	}
	wrong = result
	wrong.Digest[0] ^= 1
	if _, err := BuildSignedV2Transaction(context.Background(), config, wrong, signer); err == nil {
		t.Fatal("accepted wrong certificate digest")
	}
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about" // TEST-ONLY
	if _, err := signer.Keyring.NewAccount("bob", mnemonic, "", "m/44'/118'/1'/0/0", hd.Secp256k1); err != nil {
		t.Fatal(err)
	}
	signer.Name = "bob"
	if _, err := BuildSignedV2Transaction(context.Background(), config, result, signer); err == nil {
		t.Fatal("accepted another account signer")
	}
}

func assertSDKSignature(t *testing.T, config client.TxConfig, tx sdk.Tx, result CertificateIssueResultV2, valid bool) {
	t.Helper()
	sigs, err := tx.(interface {
		GetSignaturesV2() ([]signing.SignatureV2, error)
	}).GetSignaturesV2()
	if err != nil || len(sigs) != 1 {
		t.Fatal("missing account signature")
	}
	bytesToSign, err := authsigning.GetSignBytesAdapter(context.Background(), config.SignModeHandler(), signing.SignMode_SIGN_MODE_DIRECT,
		authsigning.SignerData{ChainID: result.Intent.ChainID, AccountNumber: result.Intent.AccountNumber, Sequence: sigs[0].Sequence, Address: result.Intent.Subject, PubKey: sigs[0].PubKey}, tx)
	if err != nil {
		t.Fatal(err)
	}
	signature := sigs[0].Data.(*signing.SingleSignatureData).Signature
	if got := sigs[0].PubKey.VerifySignature(bytesToSign, signature); got != valid {
		t.Fatalf("SDK signature valid = %v, want %v", got, valid)
	}
}
