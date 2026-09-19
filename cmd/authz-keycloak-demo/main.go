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
	"math"
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

const (
	integrationIssuerSetID = uint64(9)
	integrationDenom       = "token"
	integrationMaxAmount   = "5000"
	policyDescriptorDomain = "alpha.keycloak.attribute-policy.v1"
)

type options struct {
	batchID         uint64
	subject         string
	receiver        string
	amount          string
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
	policyVersionNumber  uint64
	permission           string
}

type commandConfig struct {
	options     options
	environment environment
}

type commandOutput struct {
	AuthorizationID string `json:"authorization_id"`
	BatchID         uint64 `json:"batch_id"`
	BatchHash       string `json:"batch_hash"`
	TxHash          string `json:"tx_hash"`
	Height          int64  `json:"height"`
	Subject         string `json:"subject"`
	Receiver        string `json:"receiver"`
	Denom           string `json:"denom"`
	MaxAmount       string `json:"max_amount"`
	PolicyID        string `json:"policy_id"`
	PolicyVersion   uint64 `json:"policy_version"`
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

	keycloak := remote.NewKeycloakClient(remote.KeycloakConfig{
		BaseURL:                     config.environment.keycloakBaseURL,
		Realm:                       config.environment.keycloakRealm,
		ClientID:                    config.environment.keycloakClientID,
		ClientSecret:                config.environment.keycloakClientSecret,
		EnableWalletAttributeLookup: config.environment.enableWalletLookup,
		WalletAttributeName:         config.environment.walletAttribute,
	})
	publisher, err := authorization.NewAlphadBatchPublisher(authorization.AlphadPublisherConfig{
		BinaryPath:     config.options.alphad,
		From:           config.options.submitter,
		ChainID:        config.options.chainID,
		KeyringBackend: config.options.keyringBackend,
		Home:           config.options.home,
		Node:           config.options.node,
	}, nil, nil)
	if err != nil {
		return err
	}
	confirmer, err := authorization.NewAlphadBatchCommitConfirmer(authorization.AlphadCommitConfig{
		BinaryPath:   config.options.alphad,
		Submitter:    config.options.submitter,
		Home:         config.options.home,
		Node:         config.options.node,
		MaxAttempts:  config.options.maxAttempts,
		PollInterval: config.options.pollInterval,
	}, nil, nil)
	if err != nil {
		return err
	}
	issuer, err := authorization.NewAuthorizationIssuer(
		keycloak,
		config.policyEvaluator(),
		[]authorization.BatchSigner{alphaSigner, betaSigner},
		publisher,
		confirmer,
		nil,
	)
	if err != nil {
		return err
	}
	result, err := issuer.Issue(ctx, config.issueRequest())
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(config.output(result))
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
	flags := flag.NewFlagSet("authz-keycloak-demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Uint64Var(&parsed.batchID, "batch-id", 0, "positive authorization batch ID")
	flags.StringVar(&parsed.subject, "subject", "", "authorized Cosmos account")
	flags.StringVar(&parsed.receiver, "receiver", "", "allowed Cosmos receiver")
	flags.StringVar(&parsed.amount, "amount", "", "requested token amount")
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
		"subject": parsed.subject, "receiver": parsed.receiver, "amount": parsed.amount,
		"submitter": parsed.submitter, "alphad": parsed.alphad, "chain-id": parsed.chainID,
		"keyring-backend": parsed.keyringBackend, "issuer-alpha-seed-file": parsed.issuerAlphaSeed,
		"issuer-beta-seed-file": parsed.issuerBetaSeed,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return options{}, fmt.Errorf("--%s is required", name)
		}
	}
	if parsed.batchID == 0 {
		return options{}, fmt.Errorf("--batch-id must be positive")
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
	env.policyVersionNumber, err = strconv.ParseUint(env.policyVersion, 10, 64)
	if err != nil || env.policyVersionNumber == 0 || strconv.FormatUint(env.policyVersionNumber, 10) != env.policyVersion {
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

func (c commandConfig) policyEvaluator() policy.AttributeEvaluator {
	return policy.AttributeEvaluator{
		PolicyID:           c.environment.policyID,
		PolicyVersion:      c.environment.policyVersion,
		Operation:          authorization.MsgSendTypeURL,
		RequiredPermission: c.environment.permission,
	}
}

func (c commandConfig) issueRequest() authorization.AuthorizationIssueRequest {
	policyHash := configuredPolicyHash(c.environment.policyID, c.environment.policyVersion, c.environment.permission)
	return authorization.AuthorizationIssueRequest{
		Facts: authorization.NormalizedMsgSendFacts{
			Subject: c.options.subject, MsgTypeURL: authorization.MsgSendTypeURL,
			Receiver: c.options.receiver, Denom: integrationDenom, Amount: c.options.amount,
		},
		AuthorizationContext: authorization.TrustedAuthorizationContext{
			AuthorizationID: fmt.Sprintf("keycloak-bank-send-%d", c.options.batchID),
			IssuerSetID:     integrationIssuerSetID, ValidFromHeight: 1, ValidUntilHeight: math.MaxInt64,
			AllowedDenom: integrationDenom, AllowedReceiver: c.options.receiver, MaxAmount: integrationMaxAmount,
		},
		BatchContext: authorization.TrustedBatchContext{
			ChainID: c.options.chainID, BatchID: c.options.batchID,
			PolicyID: c.environment.policyID, PolicyVersion: c.environment.policyVersionNumber,
			PolicyHash: policyHash[:], IssuerSetID: integrationIssuerSetID,
		},
	}
}

func configuredPolicyDescriptor(policyID, policyVersion, permission string) string {
	return policyDescriptorDomain +
		"|policy_id=" + policyID +
		"|policy_version=" + policyVersion +
		"|operation=" + authorization.MsgSendTypeURL +
		"|permission=" + permission +
		"|denom=" + integrationDenom +
		"|max_amount=" + integrationMaxAmount
}

func configuredPolicyHash(policyID, policyVersion, permission string) [sha256.Size]byte {
	return sha256.Sum256([]byte(configuredPolicyDescriptor(policyID, policyVersion, permission)))
}

func (c commandConfig) output(result authorization.AuthorizationIssueResult) commandOutput {
	record := result.AuthorizationRecord
	return commandOutput{
		AuthorizationID: record.AuthorizationID,
		BatchID:         c.options.batchID,
		BatchHash:       hex.EncodeToString(result.BatchHash[:]),
		TxHash:          result.TxHash,
		Height:          result.Height,
		Subject:         record.Subject,
		Receiver:        record.BankSendConstraints.Receiver,
		Denom:           record.BankSendConstraints.Denom,
		MaxAmount:       record.BankSendConstraints.MaxAmount,
		PolicyID:        record.PolicyID,
		PolicyVersion:   record.PolicyVersion,
	}
}
