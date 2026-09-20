package main

import (
	"context"
	"crypto/ed25519"
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

	"spaghetti/internal/authorization"
	remote "spaghetti/internal/remote/keycloak"
	"spaghetti/internal/remote/policy"
)

type options struct {
	subject         string
	submitter       string
	alphad          string
	chainID         string
	keyringBackend  string
	issuerAlphaSeed string
	issuerBetaSeed  string
	home            string
	node            string
	maxAttempts     int
	pollInterval    time.Duration
}

type environment struct {
	keycloakBaseURL      string
	keycloakRealm        string
	keycloakClientID     string
	keycloakClientSecret string
	enableWalletLookup   bool
	walletAttribute      string
	policyID             string
	policyVersion        string
	permission           string
}

type commandConfig struct {
	options     options
	environment environment
}

type commandOutput struct {
	Status          authorization.AuthorizationReconcileStatus `json:"status"`
	AuthorizationID string                                     `json:"authorization_id,omitempty"`
	BatchID         uint64                                     `json:"batch_id,omitempty"`
	BatchHash       string                                     `json:"batch_hash,omitempty"`
	TxHash          string                                     `json:"tx_hash,omitempty"`
	Height          int64                                      `json:"height,omitempty"`
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
	config, err := loadCommandConfig(args, getenv)
	if err != nil {
		return err
	}
	alphaSeed, err := authorization.LoadDemoIssuerSeed(config.options.issuerAlphaSeed)
	if err != nil {
		return fmt.Errorf("load issuer-alpha seed: %w", err)
	}
	betaSeed, err := authorization.LoadDemoIssuerSeed(config.options.issuerBetaSeed)
	if err != nil {
		clear(alphaSeed)
		return fmt.Errorf("load issuer-beta seed: %w", err)
	}
	defer clear(alphaSeed)
	defer clear(betaSeed)

	alphaKey := ed25519.NewKeyFromSeed(alphaSeed)
	betaKey := ed25519.NewKeyFromSeed(betaSeed)
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

	keycloak := remote.NewKeycloakClient(config.keycloakConfig())
	reader, err := authorization.NewAlphadAuthorizationStateReader(config.stateReaderConfig(), nil)
	if err != nil {
		return err
	}
	publisher, err := authorization.NewAlphadBatchPublisher(authorization.AlphadPublisherConfig{
		BinaryPath: config.options.alphad, From: config.options.submitter,
		ChainID: config.options.chainID, KeyringBackend: config.options.keyringBackend,
		Home: config.options.home, Node: config.options.node,
	}, nil, nil)
	if err != nil {
		return err
	}
	confirmer, err := authorization.NewAlphadBatchCommitConfirmer(authorization.AlphadCommitConfig{
		BinaryPath: config.options.alphad, Submitter: config.options.submitter,
		Home: config.options.home, Node: config.options.node,
		MaxAttempts: config.options.maxAttempts, PollInterval: config.options.pollInterval,
	}, nil, nil)
	if err != nil {
		return err
	}
	reconciler, err := authorization.NewAuthorizationRevocationReconciler(
		keycloak, config.policyEvaluator(), reader,
		[]authorization.BatchSigner{alphaSigner, betaSigner}, publisher, confirmer, nil,
	)
	if err != nil {
		return err
	}
	result, err := reconciler.Reconcile(ctx, config.reconcileRequest())
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(output(result))
}

func loadCommandConfig(args []string, getenv func(string) string) (commandConfig, error) {
	parsed, err := parseOptions(args)
	if err != nil {
		return commandConfig{}, err
	}
	env, err := loadEnvironment(getenv)
	if err != nil {
		return commandConfig{}, err
	}
	return commandConfig{options: parsed, environment: env}, nil
}

func parseOptions(args []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("authz-reconcile-demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.subject, "subject", "", "authorized Cosmos account")
	flags.StringVar(&parsed.submitter, "submitter", "", "Cosmos transaction broadcaster")
	flags.StringVar(&parsed.alphad, "alphad", "", "absolute alphad binary path")
	flags.StringVar(&parsed.chainID, "chain-id", "", "Alpha chain ID")
	flags.StringVar(&parsed.keyringBackend, "keyring-backend", "", "alphad keyring backend")
	flags.StringVar(&parsed.issuerAlphaSeed, "issuer-alpha-seed-file", "", "issuer-alpha Ed25519 seed file")
	flags.StringVar(&parsed.issuerBetaSeed, "issuer-beta-seed-file", "", "issuer-beta Ed25519 seed file")
	flags.StringVar(&parsed.home, "home", "", "optional alphad home")
	flags.StringVar(&parsed.node, "node", "", "optional Alpha RPC node")
	flags.IntVar(&parsed.maxAttempts, "max-attempts", 20, "maximum commit queries")
	flags.DurationVar(&parsed.pollInterval, "poll-interval", 500*time.Millisecond, "commit query interval")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments")
	}
	required := map[string]string{
		"subject": parsed.subject, "submitter": parsed.submitter, "alphad": parsed.alphad,
		"chain-id": parsed.chainID, "keyring-backend": parsed.keyringBackend,
		"issuer-alpha-seed-file": parsed.issuerAlphaSeed, "issuer-beta-seed-file": parsed.issuerBetaSeed,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return options{}, fmt.Errorf("--%s is required", name)
		}
	}
	if !filepath.IsAbs(parsed.alphad) {
		return options{}, fmt.Errorf("--alphad must be an absolute path")
	}
	if parsed.maxAttempts <= 0 {
		return options{}, fmt.Errorf("--max-attempts must be positive")
	}
	if parsed.pollInterval < 0 {
		return options{}, fmt.Errorf("--poll-interval must not be negative")
	}
	return parsed, nil
}

func loadEnvironment(getenv func(string) string) (environment, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", fmt.Errorf("missing required environment variable %s", name)
		}
		return value, nil
	}
	var env environment
	var err error
	if env.keycloakBaseURL, err = required("SPAGHETTI_KEYCLOAK_BASE_URL"); err != nil {
		return environment{}, err
	}
	if env.keycloakRealm, err = required("SPAGHETTI_KEYCLOAK_REALM"); err != nil {
		return environment{}, err
	}
	if env.keycloakClientID, err = required("SPAGHETTI_KEYCLOAK_CLIENT_ID"); err != nil {
		return environment{}, err
	}
	if env.keycloakClientSecret, err = required("SPAGHETTI_KEYCLOAK_CLIENT_SECRET"); err != nil {
		return environment{}, err
	}
	if env.policyID, err = required("SPAGHETTI_POLICY_ID"); err != nil {
		return environment{}, err
	}
	if env.policyVersion, err = required("SPAGHETTI_POLICY_VERSION"); err != nil {
		return environment{}, err
	}
	if env.permission, err = required("SPAGHETTI_POLICY_SEND_PERMISSION"); err != nil {
		return environment{}, err
	}
	if strings.ContainsAny(env.policyID+env.permission, "|\r\n") {
		return environment{}, fmt.Errorf("policy identity and permission must not contain descriptor delimiters")
	}
	version, err := strconv.ParseUint(env.policyVersion, 10, 64)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != env.policyVersion {
		return environment{}, fmt.Errorf("SPAGHETTI_POLICY_VERSION must be a canonical positive uint64")
	}
	env.walletAttribute = strings.TrimSpace(getenv("SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE"))
	if raw := strings.TrimSpace(getenv("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP")); raw != "" {
		env.enableWalletLookup, err = strconv.ParseBool(raw)
		if err != nil {
			return environment{}, fmt.Errorf("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP must be a boolean")
		}
	}
	return env, nil
}

func (c commandConfig) keycloakConfig() remote.KeycloakConfig {
	return remote.KeycloakConfig{
		BaseURL: c.environment.keycloakBaseURL, Realm: c.environment.keycloakRealm,
		ClientID: c.environment.keycloakClientID, ClientSecret: c.environment.keycloakClientSecret,
		EnableWalletAttributeLookup: c.environment.enableWalletLookup,
		WalletAttributeName:         c.environment.walletAttribute,
	}
}

func (c commandConfig) policyEvaluator() policy.AttributeEvaluator {
	return policy.AttributeEvaluator{
		PolicyID: c.environment.policyID, PolicyVersion: c.environment.policyVersion,
		Operation: authorization.MsgSendTypeURL, RequiredPermission: c.environment.permission,
	}
}

func (c commandConfig) stateReaderConfig() authorization.AlphadAuthorizationStateReaderConfig {
	return authorization.AlphadAuthorizationStateReaderConfig{
		BinaryPath: c.options.alphad, Home: c.options.home, Node: c.options.node,
	}
}

func (c commandConfig) reconcileRequest() authorization.AuthorizationReconcileRequest {
	policyHash := authorization.KeycloakPolicyHash(c.environment.policyID, c.environment.policyVersion, c.environment.permission)
	return authorization.AuthorizationReconcileRequest{
		Subject: c.options.subject, MsgTypeURL: authorization.MsgSendTypeURL,
		ChainID: c.options.chainID, PolicyHash: policyHash[:],
	}
}

func output(result authorization.AuthorizationReconcileResult) commandOutput {
	out := commandOutput{Status: result.Status}
	if result.Status == authorization.ReconcileRevoked {
		out.AuthorizationID = result.AuthorizationID
		out.BatchID = result.BatchID
		out.BatchHash = hex.EncodeToString(result.BatchHash[:])
		out.TxHash = result.TxHash
		out.Height = result.Height
	}
	return out
}
