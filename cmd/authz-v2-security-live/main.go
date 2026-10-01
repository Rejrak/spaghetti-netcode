// Command authz-v2-security-live produces local RPC evidence; no production path imports it.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	cmtrpc "github.com/cometbft/cometbft/rpc/client/http"
	rpctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/protobuf/proto"
	"spaghetti/internal/authorization"
	v2pb "spaghetti/internal/authorization/pb/v2"
)

type options struct {
	alpha, home, node, chainID, alice, receiver, alphaSeed, betaSeed string
	policyID, policyVersion, permission                              string
}

type liveRPC interface {
	BroadcastTxSync(context.Context, cmttypes.Tx) (*rpctypes.ResultBroadcastTx, error)
	Tx(context.Context, []byte, bool) (*rpctypes.ResultTx, error)
	Status(context.Context) (*rpctypes.ResultStatus, error)
}

type observation struct {
	Scenario                  string  `json:"scenario"`
	ExpectedLayer             string  `json:"expected_layer"`
	BroadcastCount            int     `json:"broadcast_count"`
	TxHash                    string  `json:"tx_hash,omitempty"`
	CheckTxCode               *uint32 `json:"checktx_code,omitempty"`
	CheckTxCodespace          string  `json:"checktx_codespace,omitempty"`
	CheckTxLog                string  `json:"checktx_log,omitempty"`
	ObservedReason            string  `json:"observed_reason,omitempty"`
	ObservedLayer             string  `json:"observed_layer,omitempty"`
	RPCError                  string  `json:"rpc_error,omitempty"`
	Committed                 *bool   `json:"committed,omitempty"`
	CommitHeight              int64   `json:"commit_height,omitempty"`
	DeliverCode               *uint32 `json:"deliver_code,omitempty"`
	SenderSequenceBefore      *uint64 `json:"sender_sequence_before,omitempty"`
	SenderSequenceAfter       *uint64 `json:"sender_sequence_after,omitempty"`
	SenderBalanceBefore       string  `json:"sender_balance_before,omitempty"`
	SenderBalanceAfter        string  `json:"sender_balance_after,omitempty"`
	ReceiverBalanceBefore     string  `json:"receiver_balance_before,omitempty"`
	ReceiverBalanceAfter      string  `json:"receiver_balance_after,omitempty"`
	ReplayCreatedSecondCommit *bool   `json:"replay_created_second_commit,omitempty"`
}

var v2ReasonPattern = regexp.MustCompile(`AUTHZ_V2_[A-Z_]+`)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("authz-v2-security-live", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	f.StringVar(&o.alpha, "alphad", "", "absolute alphad path")
	f.StringVar(&o.home, "home", "", "Alpha keyring home")
	f.StringVar(&o.node, "node", "tcp://127.0.0.1:26657", "Alpha RPC")
	f.StringVar(&o.chainID, "chain-id", "", "expected chain ID")
	f.StringVar(&o.alice, "alice", "alice", "local bootstrap key name")
	f.StringVar(&o.receiver, "receiver", "", "receiver Cosmos address")
	f.StringVar(&o.alphaSeed, "issuer-alpha-seed-file", "", "0600 demo seed path")
	f.StringVar(&o.betaSeed, "issuer-beta-seed-file", "", "0600 demo seed path")
	f.StringVar(&o.policyID, "policy-id", "policy-bank-send", "trusted local policy ID")
	f.StringVar(&o.policyVersion, "policy-version", "1", "trusted local policy version")
	f.StringVar(&o.permission, "permission", "supply.transaction.send", "trusted local permission")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || !strings.HasPrefix(o.alpha, "/") || o.home == "" || o.chainID == "" ||
		o.alphaSeed == "" || o.betaSeed == "" || o.policyID == "" || o.permission == "" ||
		strings.ContainsAny(o.policyID+o.permission, "|\r\n") || authorization.ValidateAccountAddress(o.receiver) != nil {
		return o, fmt.Errorf("missing or invalid local security harness flags")
	}
	return o, nil
}

func run(ctx context.Context, args []string) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	version, err := strconv.ParseUint(o.policyVersion, 10, 64)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != o.policyVersion {
		return fmt.Errorf("invalid policy version")
	}
	rpc, err := cmtrpc.New(o.node, "/websocket")
	if err != nil {
		return err
	}
	state, err := authorization.NewAlphadAccountStateProviderV2(authorization.AlphadAuthorizationStateReaderConfig{
		BinaryPath: o.alpha, Home: o.home, Node: o.node,
	}, nil)
	if err != nil {
		return err
	}
	txConfig, err := authorization.NewCosmosTxConfigV2()
	if err != nil {
		return err
	}
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	keyCodec := codec.NewProtoCodec(registry)
	aliceKeys, err := keyring.New(sdk.KeyringServiceName(), keyring.BackendTest, o.home, os.Stdin, keyCodec)
	if err != nil {
		return err
	}
	aliceRecord, err := aliceKeys.Key(o.alice)
	if err != nil {
		return err
	}
	aliceAddress, err := aliceRecord.GetAddress()
	if err != nil {
		return err
	}
	ephemeralKeys := keyring.NewInMemory(keyCodec)
	ephemeralRecord, _, err := ephemeralKeys.NewMnemonic("isolated-v2-security", keyring.English, sdk.FullFundraiserPath, keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	if err != nil {
		return err
	}
	ephemeralAddress, err := ephemeralRecord.GetAddress()
	if err != nil {
		return err
	}
	signers := make([]authorization.CertificateSignerV2, 0, 2)
	for _, entry := range []struct{ id, path string }{{"issuer-alpha", o.alphaSeed}, {"issuer-beta", o.betaSeed}} {
		seed, err := authorization.LoadDemoIssuerSeed(entry.path)
		if err != nil {
			return fmt.Errorf("load demo issuer seed: %w", err)
		}
		privateKey := ed25519.NewKeyFromSeed(seed)
		clear(seed)
		signer, err := authorization.NewEd25519BatchSigner(entry.id, privateKey)
		clear(privateKey)
		if err != nil {
			return err
		}
		signers = append(signers, signer)
	}
	hash := sha256.Sum256([]byte("alpha.keycloak.attribute-policy.v2|policy_id=" + o.policyID +
		"|policy_version=" + o.policyVersion + "|operation=" + authorization.MsgSendTypeURL +
		"|permission=" + o.permission + "|issuer_set_id=9"))
	trusted := authorization.TrustedCertificateContextV2{
		ChainID: o.chainID, PolicyID: o.policyID, PolicyVersion: version, PolicyHash: hash[:], IssuerSetID: 9,
	}
	// Bootstrap only: Alice funds the in-memory sender through a valid V2 transaction.
	aliceState, err := state.AccountState(ctx, aliceAddress.String())
	if err != nil {
		return err
	}
	if aliceState.ChainID != o.chainID {
		return fmt.Errorf("chain ID mismatch")
	}
	fundingIntent := intent(aliceState, aliceAddress.String(), ephemeralAddress.String(), "1", "security-live funding")
	fundingCert, err := signedCertificate(ctx, fundingIntent, trusted, aliceState.CurrentHeight, aliceState.CurrentHeight+40, signers)
	if err != nil {
		return err
	}
	fundingTx, err := signedTx(ctx, txConfig, aliceKeys, o.alice, fundingIntent, fundingCert)
	if err != nil {
		return err
	}
	funding, err := broadcastOnce(ctx, rpc, fundingTx, "BOOTSTRAP_FUNDING", "Alpha V2 and Cosmos SDK")
	if err != nil || funding.CheckTxCode == nil || *funding.CheckTxCode != 0 {
		return fmt.Errorf("bootstrap funding CheckTx failed: %v", err)
	}
	if err := waitCommitted(ctx, rpc, &funding); err != nil || funding.DeliverCode == nil || *funding.DeliverCode != 0 {
		return fmt.Errorf("bootstrap funding not committed: %v", err)
	}
	// Query the new account only after funding has committed.
	senderState, err := state.AccountState(ctx, ephemeralAddress.String())
	if err != nil {
		return err
	}
	if senderState.ChainID != o.chainID {
		return fmt.Errorf("isolated sender chain ID mismatch")
	}
	if senderState.CurrentHeight <= 2 {
		return fmt.Errorf("Alpha height too low for expired fixture")
	}
	baseIntent := intent(senderState, ephemeralAddress.String(), o.receiver, "1", "security-live negative")
	validCert, err := signedCertificate(ctx, baseIntent, trusted, senderState.CurrentHeight, senderState.CurrentHeight+40, signers)
	if err != nil {
		return err
	}
	malformedCert, err := malformedCertificate(validCert)
	if err != nil {
		return err
	}
	malformedTx, err := signedTx(ctx, txConfig, ephemeralKeys, "isolated-v2-security", baseIntent, malformedCert)
	if err != nil {
		return err
	}
	malformed, err := broadcastOnce(ctx, rpc, malformedTx, "MALFORMED_CERTIFICATE", "Alpha V2 certificate structural check")
	if err != nil {
		return err
	}
	if malformed.CheckTxCode == nil || *malformed.CheckTxCode == 0 {
		return fmt.Errorf("malformed certificate accepted by CheckTx")
	}
	if err := confirmRejected(ctx, rpc, &malformed); err != nil {
		return err
	}
	expiredCert, err := signedCertificate(ctx, baseIntent, trusted, senderState.CurrentHeight-2, senderState.CurrentHeight-1, signers)
	if err != nil {
		return err
	}
	expiredTx, err := signedTx(ctx, txConfig, ephemeralKeys, "isolated-v2-security", baseIntent, expiredCert)
	if err != nil {
		return err
	}
	expired, err := broadcastOnce(ctx, rpc, expiredTx, "EXPIRED_CERTIFICATE", "Alpha V2 height validity")
	if err != nil {
		return err
	}
	if expired.CheckTxCode == nil || *expired.CheckTxCode == 0 {
		return fmt.Errorf("expired certificate accepted by CheckTx")
	}
	if err := confirmRejected(ctx, rpc, &expired); err != nil {
		return err
	}
	// A fresh certificate is required after bounded negative-query waits.
	senderState, err = state.AccountState(ctx, ephemeralAddress.String())
	if err != nil {
		return err
	}
	baseIntent = intent(senderState, ephemeralAddress.String(), o.receiver, "1", "security-live replay")
	validCert, err = signedCertificate(ctx, baseIntent, trusted, senderState.CurrentHeight, senderState.CurrentHeight+40, signers)
	if err != nil {
		return err
	}
	replayRaw, err := signedTx(ctx, txConfig, ephemeralKeys, "isolated-v2-security", baseIntent, validCert)
	if err != nil {
		return err
	}
	senderBefore, err := balance(ctx, o, ephemeralAddress.String())
	if err != nil {
		return err
	}
	receiverBefore, err := balance(ctx, o, o.receiver)
	if err != nil {
		return err
	}
	first, err := broadcastOnce(ctx, rpc, replayRaw, "REPLAY_CONTROL", "Alpha V2 and Cosmos SDK")
	if err != nil || first.CheckTxCode == nil || *first.CheckTxCode != 0 {
		return fmt.Errorf("replay control CheckTx failed: %v", err)
	}
	if err := waitCommitted(ctx, rpc, &first); err != nil || first.DeliverCode == nil || *first.DeliverCode != 0 {
		return fmt.Errorf("replay control not committed: %v", err)
	}
	afterFirst, err := state.AccountState(ctx, ephemeralAddress.String())
	if err != nil {
		return err
	}
	senderAfterFirst, err := balance(ctx, o, ephemeralAddress.String())
	if err != nil {
		return err
	}
	receiverAfterFirst, err := balance(ctx, o, o.receiver)
	if err != nil {
		return err
	}
	if afterFirst.Sequence != senderState.Sequence+1 || !deltaIs(senderBefore, senderAfterFirst, "-1") || !deltaIs(receiverBefore, receiverAfterFirst, "1") {
		return fmt.Errorf("replay control did not transfer exactly once")
	}
	first.SenderSequenceBefore, first.SenderSequenceAfter = &senderState.Sequence, &afterFirst.Sequence
	first.SenderBalanceBefore, first.SenderBalanceAfter = senderBefore, senderAfterFirst
	first.ReceiverBalanceBefore, first.ReceiverBalanceAfter = receiverBefore, receiverAfterFirst
	replay, err := broadcastOnce(ctx, rpc, replayRaw, "EXACT_RAW_TX_REPLAY", "Cosmos SDK account sequence")
	if err != nil {
		return err
	}
	if err := waitNextBlock(ctx, rpc); err != nil {
		return err
	}
	afterReplay, err := state.AccountState(ctx, ephemeralAddress.String())
	if err != nil {
		return err
	}
	senderAfterReplay, err := balance(ctx, o, ephemeralAddress.String())
	if err != nil {
		return err
	}
	receiverAfterReplay, err := balance(ctx, o, o.receiver)
	if err != nil {
		return err
	}
	secondCommit := afterReplay.Sequence != afterFirst.Sequence || senderAfterReplay != senderAfterFirst || receiverAfterReplay != receiverAfterFirst
	replay.ReplayCreatedSecondCommit = &secondCommit
	replay.SenderSequenceBefore, replay.SenderSequenceAfter = &afterFirst.Sequence, &afterReplay.Sequence
	replay.SenderBalanceBefore, replay.SenderBalanceAfter = senderAfterFirst, senderAfterReplay
	replay.ReceiverBalanceBefore, replay.ReceiverBalanceAfter = receiverAfterFirst, receiverAfterReplay
	if secondCommit {
		return fmt.Errorf("replay changed sender state or balances")
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		SenderAddress string        `json:"sender_address"`
		Bootstrap     observation   `json:"bootstrap"`
		ReplayControl observation   `json:"replay_control"`
		Scenarios     []observation `json:"scenarios"`
	}{ephemeralAddress.String(), funding, first, []observation{malformed, expired, replay}}); err != nil {
		return err
	}
	if replay.CheckTxCode == nil {
		return fmt.Errorf("exact replay stopped in CometBFT cache before SDK CheckTx")
	}
	if *replay.CheckTxCode == 0 {
		return fmt.Errorf("exact raw replay accepted by CheckTx")
	}
	if malformed.ObservedReason != "AUTHZ_V2_INVALID_CERTIFICATE" || expired.ObservedReason != "AUTHZ_V2_EXPIRED" ||
		(replay.ObservedReason != "account sequence mismatch" && replay.ObservedReason != "incorrect account sequence") {
		return fmt.Errorf("live CheckTx reason differs from expected class")
	}
	return nil
}

func intent(state authorization.AlphaAccountStateV2, subject, receiver, amount, memo string) authorization.AuthorizationIntentV2 {
	return authorization.AuthorizationIntentV2{ChainID: state.ChainID, Subject: subject, Receiver: receiver,
		Denom: "token", Amount: amount, AccountNumber: state.AccountNumber, Sequence: state.Sequence,
		Memo: memo, GasLimit: 250000}
}

func signedCertificate(ctx context.Context, intent authorization.AuthorizationIntentV2, trusted authorization.TrustedCertificateContextV2, from, until int64, signers []authorization.CertificateSignerV2) ([]byte, error) {
	trusted.ValidFromHeight, trusted.ValidUntilHeight = from, until
	certificate, _, _, err := authorization.BuildAuthorizationCertificateV2(ctx, intent, trusted, signers)
	if err != nil {
		return nil, err
	}
	return authorization.MarshalAuthorizationCertificateV2(certificate)
}

func malformedCertificate(valid []byte) ([]byte, error) {
	var certificate v2pb.AuthorizationCertificateV2
	if err := proto.Unmarshal(valid, &certificate); err != nil || certificate.SignDoc == nil {
		return nil, fmt.Errorf("invalid fixture input")
	}
	certificate.SignDoc.Intent = nil // Same wire-decodable invalid class as Alpha's Ante test.
	return proto.Marshal(&certificate)
}

func signedTx(ctx context.Context, config client.TxConfig, keys keyring.Keyring, name string, intent authorization.AuthorizationIntentV2, certificate []byte) ([]byte, error) {
	amount, err := strconv.ParseInt(intent.Amount, 10, 64)
	if err != nil || amount <= 0 {
		return nil, fmt.Errorf("invalid transfer amount")
	}
	builder := config.NewTxBuilder()
	if err := builder.SetMsgs(&banktypes.MsgSend{FromAddress: intent.Subject, ToAddress: intent.Receiver,
		Amount: sdk.NewCoins(sdk.NewInt64Coin(intent.Denom, amount))}); err != nil {
		return nil, err
	}
	builder.SetGasLimit(intent.GasLimit)
	builder.SetMemo(intent.Memo)
	builder.SetTimeoutHeight(intent.TimeoutHeight)
	extended, ok := builder.(client.ExtendedTxBuilder)
	if !ok {
		return nil, fmt.Errorf("critical extension unsupported")
	}
	extended.SetExtensionOptions(&codectypes.Any{TypeUrl: authorization.CertificateTypeURLV2, Value: certificate})
	factory := clienttx.Factory{}.WithTxConfig(config).WithKeybase(keys).WithChainID(intent.ChainID).
		WithAccountNumber(intent.AccountNumber).WithSequence(intent.Sequence).WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)
	if err := clienttx.Sign(ctx, factory, name, builder, true); err != nil {
		return nil, err
	}
	return config.TxEncoder()(builder.GetTx())
}

func broadcastOnce(ctx context.Context, rpc liveRPC, raw []byte, scenario, layer string) (observation, error) {
	response, err := rpc.BroadcastTxSync(ctx, cmttypes.Tx(raw))
	if err != nil {
		if strings.Contains(err.Error(), "tx already exists in cache") {
			return observation{Scenario: scenario, ExpectedLayer: layer, BroadcastCount: 1,
				ObservedLayer: "CometBFT mempool cache", ObservedReason: "tx already exists in cache",
				RPCError: "tx already exists in cache"}, nil
		}
		return observation{}, fmt.Errorf("broadcast %s: %w", scenario, err)
	}
	if response == nil {
		return observation{}, fmt.Errorf("empty broadcast response for %s", scenario)
	}
	expected := sha256.Sum256(raw)
	if !bytes.Equal(response.Hash, expected[:]) {
		return observation{}, fmt.Errorf("broadcast hash mismatch")
	}
	result := observation{Scenario: scenario, ExpectedLayer: layer, BroadcastCount: 1,
		TxHash: strings.ToUpper(hex.EncodeToString(response.Hash)), CheckTxCode: &response.Code,
		CheckTxCodespace: response.Codespace, ObservedLayer: "CheckTx"}
	if reason := v2ReasonPattern.FindString(response.Log); reason != "" {
		result.ObservedReason, result.CheckTxLog = reason, reason
	} else {
		for _, reason := range []string{"account sequence mismatch", "incorrect account sequence"} {
			if strings.Contains(strings.ToLower(response.Log), reason) {
				result.ObservedReason, result.CheckTxLog = reason, reason
				break
			}
		}
	}
	return result, nil
}

func waitCommitted(ctx context.Context, rpc liveRPC, result *observation) error {
	if result.CheckTxCode == nil || *result.CheckTxCode != 0 {
		return fmt.Errorf("cannot confirm rejected CheckTx")
	}
	hash, _ := hex.DecodeString(result.TxHash)
	for i := 0; i < 20; i++ {
		found, err := rpc.Tx(ctx, hash, false)
		if err == nil && found != nil {
			actual := sha256.Sum256(found.Tx)
			if !bytes.Equal(found.Hash, hash) || !bytes.Equal(actual[:], hash) || found.Height <= 0 {
				return fmt.Errorf("invalid committed transaction response")
			}
			committed := true
			result.Committed, result.CommitHeight = &committed, found.Height
			code := found.TxResult.Code
			result.DeliverCode = &code
			return nil
		}
		if err != nil && !notFound(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("transaction not included within 10 seconds")
}

func confirmRejected(ctx context.Context, rpc liveRPC, result *observation) error {
	if result.CheckTxCode == nil || *result.CheckTxCode == 0 {
		return fmt.Errorf("cannot classify accepted CheckTx as rejected")
	}
	if err := waitNextBlock(ctx, rpc); err != nil {
		return err
	}
	hash, _ := hex.DecodeString(result.TxHash)
	found, err := rpc.Tx(ctx, hash, false)
	if err == nil && found != nil {
		return fmt.Errorf("rejected transaction committed")
	}
	if err == nil {
		return fmt.Errorf("empty transaction query response")
	}
	if err != nil && !notFound(err) {
		return err
	}
	committed := false
	result.Committed = &committed
	return nil
}

func waitNextBlock(ctx context.Context, rpc liveRPC) error {
	start, err := rpc.Status(ctx)
	if err != nil || start == nil {
		return fmt.Errorf("query RPC height: %v", err)
	}
	for i := 0; i < 20; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		status, err := rpc.Status(ctx)
		if err != nil || status == nil {
			return fmt.Errorf("query RPC height: %v", err)
		}
		if status.SyncInfo.LatestBlockHeight > start.SyncInfo.LatestBlockHeight {
			return nil
		}
	}
	return fmt.Errorf("no new Alpha block within 10 seconds")
}

func notFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

func balance(ctx context.Context, o options, address string) (string, error) {
	args := []string{"query", "bank", "balances", address, "--output", "json", "--node", o.node}
	if o.home != "" {
		args = append(args, "--home", o.home)
	}
	output, err := exec.CommandContext(ctx, o.alpha, args...).Output()
	if err != nil {
		return "", fmt.Errorf("query balance: %w", err)
	}
	var response struct {
		Balances []struct{ Denom, Amount string } `json:"balances"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return "", err
	}
	for _, coin := range response.Balances {
		if coin.Denom == "token" {
			return coin.Amount, nil
		}
	}
	return "0", nil
}

func deltaIs(before, after, expected string) bool {
	a, okA := new(big.Int).SetString(after, 10)
	b, okB := new(big.Int).SetString(before, 10)
	e, okE := new(big.Int).SetString(expected, 10)
	return okA && okB && okE && a.Sub(a, b).Cmp(e) == 0
}
