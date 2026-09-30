package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"spaghetti/internal/authorization"
	remote "spaghetti/internal/remote/keycloak"
	"spaghetti/internal/remote/policy"
)

const demoIssuerSetID = 9

type options struct {
	subject, receiver, denom, amount, feeDenom, feeAmount      string
	memo, account, alphad, chainID, keyringBackend, home, node string
	alphaSeed, betaSeed                                        string
	gasLimit, timeoutHeight                                    uint64
	maxAttempts                                                int
	pollInterval                                               time.Duration
}

type policyConfig struct {
	baseURL, realm, clientID, clientSecret string
	policyID, policyVersion, permission    string
	version                                uint64
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	policyCfg, err := loadPolicy(getenv)
	if err != nil {
		return err
	}
	alphaSeed, err := authorization.LoadDemoIssuerSeed(opts.alphaSeed)
	if err != nil {
		return fmt.Errorf("load issuer-alpha seed: %w", err)
	}
	defer clear(alphaSeed)
	betaSeed, err := authorization.LoadDemoIssuerSeed(opts.betaSeed)
	if err != nil {
		return fmt.Errorf("load issuer-beta seed: %w", err)
	}
	defer clear(betaSeed)
	alphaKey, betaKey := ed25519.NewKeyFromSeed(alphaSeed), ed25519.NewKeyFromSeed(betaSeed)
	defer clear(alphaKey)
	defer clear(betaKey)
	alphaSigner, err := authorization.NewEd25519BatchSigner("issuer-alpha", alphaKey)
	if err != nil {
		return err
	}
	betaSigner, err := authorization.NewEd25519BatchSigner("issuer-beta", betaKey)
	if err != nil {
		return err
	}

	keycloak := remote.NewKeycloakClient(remote.KeycloakConfig{
		BaseURL: policyCfg.baseURL, Realm: policyCfg.realm,
		ClientID: policyCfg.clientID, ClientSecret: policyCfg.clientSecret,
	})
	state, err := authorization.NewAlphadAccountStateProviderV2(authorization.AlphadAuthorizationStateReaderConfig{
		BinaryPath: opts.alphad, Home: opts.home, Node: opts.node,
	}, nil)
	if err != nil {
		return err
	}
	policyHash := demoPolicyHash(policyCfg)
	issuer, err := authorization.NewCertificateIssuerV2(keycloak, policy.AttributeEvaluator{
		PolicyID: policyCfg.policyID, PolicyVersion: policyCfg.policyVersion,
		Operation: authorization.MsgSendTypeURL, RequiredPermission: policyCfg.permission,
	}, state, []authorization.CertificateSignerV2{alphaSigner, betaSigner}, authorization.CertificateIssuerV2Config{
		ChainID: opts.chainID, PolicyID: policyCfg.policyID, PolicyVersion: policyCfg.version,
		PolicyHash: policyHash[:], IssuerSetID: demoIssuerSetID, LifetimeBlocks: 40,
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
	keys, err := keyring.New(sdk.KeyringServiceName(), opts.keyringBackend, opts.home, os.Stdin, codec.NewProtoCodec(registry))
	if err != nil {
		return fmt.Errorf("open account keyring: %w", err)
	}
	rpc, err := authorization.NewCometV2TxRPC(opts.node)
	if err != nil {
		return err
	}
	broadcaster, err := authorization.NewCometV2TxBroadcaster(rpc)
	if err != nil {
		return err
	}
	confirmer, err := authorization.NewCometV2TxConfirmer(rpc, opts.maxAttempts, opts.pollInterval)
	if err != nil {
		return err
	}
	service, err := authorization.NewV2OneTxService(issuer, txConfig,
		authorization.CosmosAccountSignerV2{Keyring: keys, Name: opts.account}, broadcaster, confirmer, nil)
	if err != nil {
		return err
	}
	var fees []authorization.FeeCoinV2
	if opts.feeAmount != "0" {
		fees = []authorization.FeeCoinV2{{Denom: opts.feeDenom, Amount: opts.feeAmount}}
	}
	result, err := service.IssueAndSubmitV2(ctx, authorization.CertificateIssueRequestV2{
		Subject: opts.subject, Receiver: opts.receiver, Denom: opts.denom,
		Amount: opts.amount, TimeoutHeight: opts.timeoutHeight, Memo: opts.memo,
		FeeAmount: fees,
		GasLimit:  opts.gasLimit,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		Subject           string `json:"subject"`
		PreSequence       uint64 `json:"pre_sequence"`
		CertificateDigest string `json:"certificate_digest"`
		TxHash            string `json:"tx_hash"`
		Height            int64  `json:"height"`
		Code              uint32 `json:"code"`
		BroadcastCount    int    `json:"broadcast_count"`
	}{result.Subject, result.Sequence, hex.EncodeToString(result.CertificateDigest[:]), result.TxHash,
		result.Height, result.Code, 1})
}

func parseOptions(args []string) (options, error) {
	var out options
	f := flag.NewFlagSet("authz-v2-demo", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&out.subject, "subject", "", "Cosmos sender address")
	f.StringVar(&out.receiver, "receiver", "", "Cosmos receiver address")
	f.StringVar(&out.denom, "denom", "token", "transfer denom")
	f.StringVar(&out.amount, "amount", "", "transfer amount")
	f.StringVar(&out.feeDenom, "fee-denom", "stake", "fee denom")
	f.StringVar(&out.feeAmount, "fee-amount", "0", "fee amount; zero omits fee coins")
	f.Uint64Var(&out.gasLimit, "gas-limit", 250000, "gas limit")
	f.Uint64Var(&out.timeoutHeight, "timeout-height", 0, "transaction timeout height")
	f.StringVar(&out.memo, "memo", "", "transaction memo")
	f.StringVar(&out.account, "account", "", "sender keyring name")
	f.StringVar(&out.alphad, "alphad", "", "absolute alphad path")
	f.StringVar(&out.chainID, "chain-id", "", "expected Alpha chain ID")
	f.StringVar(&out.keyringBackend, "keyring-backend", "", "account keyring backend")
	f.StringVar(&out.home, "home", "", "alphad home")
	f.StringVar(&out.node, "node", "tcp://127.0.0.1:26657", "Alpha RPC node")
	f.StringVar(&out.alphaSeed, "issuer-alpha-seed-file", "", "local TEST issuer-alpha seed")
	f.StringVar(&out.betaSeed, "issuer-beta-seed-file", "", "local TEST issuer-beta seed")
	f.IntVar(&out.maxAttempts, "max-attempts", 20, "bounded inclusion queries")
	f.DurationVar(&out.pollInterval, "poll-interval", 500*time.Millisecond, "inclusion query interval")
	if err := f.Parse(args); err != nil {
		return options{}, err
	}
	if f.NArg() != 0 || out.subject == "" || out.receiver == "" || out.amount == "" || out.account == "" ||
		out.chainID == "" || out.keyringBackend == "" || out.home == "" || out.node == "" ||
		out.alphaSeed == "" || out.betaSeed == "" || !filepath.IsAbs(out.alphad) ||
		out.maxAttempts <= 0 || out.pollInterval < 0 {
		return options{}, fmt.Errorf("missing or invalid V2 demo transaction/infrastructure flags")
	}
	if err := authorization.ValidateAccountAddress(out.subject); err != nil {
		return options{}, fmt.Errorf("invalid subject: %w", err)
	}
	if err := authorization.ValidateAccountAddress(out.receiver); err != nil {
		return options{}, fmt.Errorf("invalid receiver: %w", err)
	}
	return out, nil
}

func loadPolicy(getenv func(string) string) (policyConfig, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", fmt.Errorf("missing required environment variable %s", name)
		}
		return value, nil
	}
	var c policyConfig
	var err error
	for _, field := range []struct {
		name string
		ptr  *string
	}{
		{"SPAGHETTI_KEYCLOAK_BASE_URL", &c.baseURL},
		{"SPAGHETTI_KEYCLOAK_REALM", &c.realm},
		{"SPAGHETTI_KEYCLOAK_CLIENT_ID", &c.clientID},
		{"SPAGHETTI_KEYCLOAK_CLIENT_SECRET", &c.clientSecret},
		{"SPAGHETTI_POLICY_ID", &c.policyID},
		{"SPAGHETTI_POLICY_VERSION", &c.policyVersion},
		{"SPAGHETTI_POLICY_SEND_PERMISSION", &c.permission},
	} {
		*field.ptr, err = required(field.name)
		if err != nil {
			return policyConfig{}, err
		}
	}
	if strings.ContainsAny(c.policyID+c.permission, "|\r\n") {
		return policyConfig{}, fmt.Errorf("invalid V2 policy descriptor delimiters")
	}
	c.version, err = strconv.ParseUint(c.policyVersion, 10, 64)
	if err != nil || c.version == 0 || strconv.FormatUint(c.version, 10) != c.policyVersion {
		return policyConfig{}, fmt.Errorf("SPAGHETTI_POLICY_VERSION must be canonical positive uint64")
	}
	return c, nil
}

// This V2-only descriptor deliberately differs from the V1 grant descriptor.
func demoPolicyHash(c policyConfig) [sha256.Size]byte {
	return sha256.Sum256([]byte("alpha.keycloak.attribute-policy.v2" +
		"|policy_id=" + c.policyID + "|policy_version=" + c.policyVersion +
		"|operation=" + authorization.MsgSendTypeURL + "|permission=" + c.permission +
		"|issuer_set_id=9"))
}
