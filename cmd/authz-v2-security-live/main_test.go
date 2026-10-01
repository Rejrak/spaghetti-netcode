package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	rpctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/gogoproto/proto"
	googleproto "google.golang.org/protobuf/proto"
	"spaghetti/internal/authorization"
	v2pb "spaghetti/internal/authorization/pb/v2"
)

const testReceiver = "cosmos1ssevndlg997a89wpw2wv9xj2vahhqcyt0gml84"

func testCertificate(t *testing.T, from, until int64) ([]byte, authorization.AuthorizationIntentV2, ed25519.PublicKey) {
	t.Helper()
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	signer, err := authorization.NewEd25519BatchSigner("issuer-alpha", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	intent := authorization.AuthorizationIntentV2{
		ChainID: "alpha-1", Subject: "cosmos1duzpxku5atm98qk6ywvgdjzn50yzv90q7c3r44",
		Receiver: testReceiver, Denom: "token", Amount: "1", AccountNumber: 7, Sequence: 0, GasLimit: 250000,
	}
	hash := sha256.Sum256([]byte("test policy"))
	encoded, err := signedCertificate(context.Background(), intent, authorization.TrustedCertificateContextV2{
		ChainID: "alpha-1", PolicyID: "policy-bank-send", PolicyVersion: 1, PolicyHash: hash[:], IssuerSetID: 9,
	}, from, until, []authorization.CertificateSignerV2{signer})
	if err != nil {
		t.Fatal(err)
	}
	return encoded, intent, publicKey
}

func TestAdversarialCertificates(t *testing.T) {
	valid, _, publicKey := testCertificate(t, 100, 104)
	malformed, err := malformedCertificate(valid)
	if err != nil {
		t.Fatal(err)
	}
	var wire v2pb.AuthorizationCertificateV2
	if err := googleproto.Unmarshal(malformed, &wire); err != nil || wire.SignDoc == nil || wire.SignDoc.Intent != nil {
		t.Fatal("malformed fixture must decode with nil intent")
	}
	if bytes.Equal(valid, malformed) {
		t.Fatal("malformed fixture unchanged")
	}
	if err := googleproto.Unmarshal(valid, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.SignDoc.ValidFromHeight > wire.SignDoc.ValidUntilHeight || wire.SignDoc.ValidUntilHeight >= 105 {
		t.Fatal("expired fixture has wrong height interval")
	}
	var certificate authorization.AuthorizationCertificateV2
	// Reconstruct sign doc through the public canonical builder and verify the wire signature.
	intent := authorization.AuthorizationIntentV2{
		ChainID: wire.SignDoc.Intent.ChainId, Subject: wire.SignDoc.Intent.Subject, Receiver: wire.SignDoc.Intent.Receiver,
		Denom: wire.SignDoc.Intent.Denom, Amount: wire.SignDoc.Intent.Amount,
		AccountNumber: wire.SignDoc.Intent.AccountNumber, Sequence: wire.SignDoc.Intent.Sequence,
		GasLimit: wire.SignDoc.Intent.GasLimit,
	}
	doc, err := authorization.BuildCertificateSignDocV2(intent, authorization.TrustedCertificateContextV2{
		ChainID: wire.SignDoc.Intent.ChainId, PolicyID: wire.SignDoc.PolicyId,
		PolicyVersion: wire.SignDoc.PolicyVersion, PolicyHash: wire.SignDoc.PolicyHash,
		IssuerSetID: wire.SignDoc.IssuerSetId, ValidFromHeight: wire.SignDoc.ValidFromHeight,
		ValidUntilHeight: wire.SignDoc.ValidUntilHeight,
	})
	if err != nil {
		t.Fatal(err)
	}
	certificate.SignDoc = doc
	signBytes, _, err := authorization.CanonicalCertificateSignBytesV2(certificate.SignDoc)
	if err != nil || !ed25519.Verify(publicKey, signBytes, wire.Signatures[0].Signature) {
		t.Fatal("expired certificate is not correctly issuer-signed")
	}
}

type fakeLiveRPC struct {
	broadcasts  [][]byte
	codes       []uint32
	logs        []string
	heights     []int64
	queries     int
	txResult    *rpctypes.ResultTx
	cacheOnCall int
}

func (f *fakeLiveRPC) BroadcastTxSync(_ context.Context, raw cmttypes.Tx) (*rpctypes.ResultBroadcastTx, error) {
	f.broadcasts = append(f.broadcasts, append([]byte(nil), raw...))
	if f.cacheOnCall == len(f.broadcasts) {
		return nil, errors.New("tx already exists in cache")
	}
	hash := sha256.Sum256(raw)
	index := len(f.broadcasts) - 1
	result := &rpctypes.ResultBroadcastTx{Hash: hash[:], Code: f.codes[index]}
	if len(f.logs) > index {
		result.Log = f.logs[index]
	}
	return result, nil
}
func (f *fakeLiveRPC) Tx(context.Context, []byte, bool) (*rpctypes.ResultTx, error) {
	f.queries++
	if f.txResult != nil {
		return f.txResult, nil
	}
	return nil, errors.New("tx not found")
}
func (f *fakeLiveRPC) Status(context.Context) (*rpctypes.ResultStatus, error) {
	height := f.heights[0]
	f.heights = f.heights[1:]
	return &rpctypes.ResultStatus{SyncInfo: rpctypes.SyncInfo{LatestBlockHeight: height}}, nil
}

func TestExactRawReplayAndCheckTxClassification(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	keys := keyring.NewInMemory(codec.NewProtoCodec(registry))
	record, _, err := keys.NewMnemonic("isolated", keyring.English, sdk.FullFundraiserPath, keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	if err != nil {
		t.Fatal(err)
	}
	address, err := record.GetAddress()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, _ := testCertificate(t, 100, 110)
	config, err := authorization.NewCosmosTxConfigV2()
	if err != nil {
		t.Fatal(err)
	}
	intent := authorization.AuthorizationIntentV2{ChainID: "alpha-1", Subject: address.String(), Receiver: testReceiver,
		Denom: "token", Amount: "1", AccountNumber: 7, Sequence: 0, GasLimit: 250000}
	// The account signature covers the final malformed or valid certificate bytes.
	raw, err := signedTx(context.Background(), config, keys, "isolated", intent, encoded)
	if err != nil {
		t.Fatal(err)
	}
	var tx txtypes.TxRaw
	if err := proto.Unmarshal(raw, &tx); err != nil || len(tx.Signatures) != 1 {
		t.Fatal("invalid signed TxRaw")
	}
	malformed, err := malformedCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	malformedRaw, err := signedTx(context.Background(), config, keys, "isolated", intent, malformed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.TxDecoder()(malformedRaw); err != nil {
		t.Fatal("wire-decodable malformed certificate did not produce SDK-decodable TxRaw:", err)
	}
	fake := &fakeLiveRPC{codes: []uint32{0, 32}, heights: []int64{100, 101},
		logs: []string{"", "account sequence mismatch " + base64.StdEncoding.EncodeToString(tx.Signatures[0])}}
	first, err := broadcastOnce(context.Background(), fake, raw, "REPLAY_CONTROL", "Cosmos SDK")
	if err != nil || first.CheckTxCode == nil || *first.CheckTxCode != 0 || first.BroadcastCount != 1 {
		t.Fatal("first broadcast classification")
	}
	replay, err := broadcastOnce(context.Background(), fake, raw, "EXACT_RAW_TX_REPLAY", "Cosmos SDK account sequence")
	if err != nil || replay.CheckTxCode == nil || *replay.CheckTxCode != 32 || replay.BroadcastCount != 1 || replay.ObservedReason != "account sequence mismatch" || len(fake.broadcasts) != 2 || !bytes.Equal(fake.broadcasts[0], fake.broadcasts[1]) {
		t.Fatal("exact replay must broadcast byte-identical TxRaw once per attempt")
	}
	if err := confirmRejected(context.Background(), fake, &replay); err != nil || replay.Committed == nil || *replay.Committed || fake.queries != 1 {
		t.Fatal("rejected CheckTx must not be classified as committed")
	}
	if err := confirmRejected(context.Background(), fake, &first); err == nil {
		t.Fatal("accepted CheckTx classified as rejection")
	}
	if err := waitCommitted(context.Background(), fake, &replay); err == nil {
		t.Fatal("rejected CheckTx classified as committed")
	}
	hash := sha256.Sum256(raw)
	fake.txResult = &rpctypes.ResultTx{Hash: hash[:], Tx: raw, Height: 101}
	if err := waitCommitted(context.Background(), fake, &first); err != nil || first.Committed == nil || !*first.Committed || first.DeliverCode == nil || *first.DeliverCode != 0 {
		t.Fatal("accepted CheckTx not confirmed as committed")
	}
	output, err := json.Marshal(replay)
	if err != nil || bytes.Contains(output, tx.Signatures[0]) || bytes.Contains(output, []byte(base64.StdEncoding.EncodeToString(tx.Signatures[0]))) ||
		bytes.Contains(output, []byte(hex.EncodeToString(tx.Signatures[0]))) || bytes.Contains(output, encoded) || bytes.Contains(output, []byte("private_key")) {
		t.Fatal("sensitive material in JSON observation")
	}
	cache := &fakeLiveRPC{cacheOnCall: 1}
	cacheResult, err := broadcastOnce(context.Background(), cache, raw, "EXACT_RAW_TX_REPLAY", "Cosmos SDK account sequence")
	if err != nil || cacheResult.CheckTxCode != nil || cacheResult.ObservedLayer != "CometBFT mempool cache" || cacheResult.BroadcastCount != 1 {
		t.Fatal("mempool cache rejection mislabeled as SDK CheckTx")
	}
}
