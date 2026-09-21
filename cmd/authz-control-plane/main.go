package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"spaghetti/internal/authorization"
	"spaghetti/internal/observability"
	remote "spaghetti/internal/remote/keycloak"
	"spaghetti/internal/remote/policy"
	"spaghetti/internal/storage/sqlite"
)

const (
	defaultCommitMaxAttempts  = 20
	defaultCommitPollInterval = 500 * time.Millisecond
)

type config struct {
	keycloakBaseURL      string
	keycloakRealm        string
	keycloakClientID     string
	keycloakClientSecret string
	enableWalletLookup   bool
	walletAttribute      string
	policyID             string
	policyVersion        string
	permission           string
	dbPath               string
	alphad               string
	chainID              string
	submitter            string
	keyringBackend       string
	reconcileInterval    time.Duration
	issuerAlphaSeed      string
	issuerBetaSeed       string
	home                 string
	node                 string
	commitMaxAttempts    int
	commitPollInterval   time.Duration
}

func (c config) String() string {
	return fmt.Sprintf("{chain_id:%q interval:%s}", c.chainID, c.reconcileInterval)
}

type controlPlaneRepo interface {
	authorization.ManagedSubjectStore
	Close() error
}

type controlPlaneScheduler interface {
	Run(context.Context) error
}

type openRepoFunc func(string) (controlPlaneRepo, error)
type buildSchedulerFunc func(config, authorization.ManagedSubjectStore, *slog.Logger) (controlPlaneScheduler, error)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, slog.Default()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	return runWith(ctx, cfg, logger, openSQLite, buildScheduler)
}

func runWith(ctx context.Context, cfg config, logger *slog.Logger, openRepo openRepoFunc, build buildSchedulerFunc) (runErr error) {
	repo, err := openRepo(cfg.dbPath)
	if err != nil {
		return fmt.Errorf("open authorization database: %w", err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close authorization database: %w", err))
		}
	}()

	scheduler, err := build(cfg, repo, logger)
	if err != nil {
		return fmt.Errorf("build authorization control plane: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(observability.EventAuthzControlPlaneStarted,
		"chain_id", cfg.chainID,
		"interval", cfg.reconcileInterval.String(),
	)
	defer logger.Info(observability.EventAuthzControlPlaneStopped,
		"chain_id", cfg.chainID,
		"interval", cfg.reconcileInterval.String(),
	)

	err = scheduler.Run(ctx)
	if err != nil && !(ctx != nil && ctx.Err() != nil && errors.Is(err, ctx.Err())) {
		return fmt.Errorf("run authorization scheduler: %w", err)
	}
	return nil
}

func openSQLite(path string) (controlPlaneRepo, error) {
	return sqlite.Open(path)
}

func buildScheduler(cfg config, store authorization.ManagedSubjectStore, logger *slog.Logger) (controlPlaneScheduler, error) {
	signers, err := loadSigners(cfg)
	if err != nil {
		return nil, err
	}
	keycloak := remote.NewKeycloakClient(cfg.keycloakConfig())
	discoverer, err := authorization.NewKeycloakSubjectDiscoverer(keycloak)
	if err != nil {
		return nil, err
	}
	reader, err := authorization.NewAlphadAuthorizationStateReader(cfg.readerConfig(), nil)
	if err != nil {
		return nil, err
	}
	publisher, err := authorization.NewAlphadBatchPublisher(cfg.publisherConfig(), nil, logger)
	if err != nil {
		return nil, err
	}
	confirmer, err := authorization.NewAlphadBatchCommitConfirmer(cfg.confirmerConfig(), nil, logger)
	if err != nil {
		return nil, err
	}
	reconciler, err := authorization.NewAuthorizationRevocationReconciler(
		keycloak, cfg.evaluator(), reader, signers, publisher, confirmer, logger,
	)
	if err != nil {
		return nil, err
	}
	worker, err := authorization.NewAuthorizationReconciliationWorker(
		discoverer, store, reconciler, cfg.workerConfig(), logger,
	)
	if err != nil {
		return nil, err
	}
	return authorization.NewAuthorizationReconciliationScheduler(worker, cfg.schedulerConfig(), logger)
}

func loadSigners(cfg config) ([]authorization.BatchSigner, error) {
	alphaSeed, err := authorization.LoadDemoIssuerSeed(cfg.issuerAlphaSeed)
	if err != nil {
		return nil, fmt.Errorf("issuer-alpha seed is unavailable or invalid")
	}
	defer clear(alphaSeed)
	betaSeed, err := authorization.LoadDemoIssuerSeed(cfg.issuerBetaSeed)
	if err != nil {
		return nil, fmt.Errorf("issuer-beta seed is unavailable or invalid")
	}
	defer clear(betaSeed)
	alphaKey := ed25519.NewKeyFromSeed(alphaSeed)
	betaKey := ed25519.NewKeyFromSeed(betaSeed)
	defer clear(alphaKey)
	defer clear(betaKey)
	alphaSigner, err := authorization.NewEd25519BatchSigner("issuer-alpha", alphaKey)
	if err != nil {
		return nil, err
	}
	betaSigner, err := authorization.NewEd25519BatchSigner("issuer-beta", betaKey)
	if err != nil {
		return nil, err
	}
	return []authorization.BatchSigner{alphaSigner, betaSigner}, nil
}

func loadConfig(getenv func(string) string) (config, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", fmt.Errorf("missing required environment variable %s", name)
		}
		return value, nil
	}
	var cfg config
	var err error
	requiredValues := []struct {
		name   string
		target *string
	}{
		{"SPAGHETTI_KEYCLOAK_BASE_URL", &cfg.keycloakBaseURL},
		{"SPAGHETTI_KEYCLOAK_REALM", &cfg.keycloakRealm},
		{"SPAGHETTI_KEYCLOAK_CLIENT_ID", &cfg.keycloakClientID},
		{"SPAGHETTI_KEYCLOAK_CLIENT_SECRET", &cfg.keycloakClientSecret},
		{"SPAGHETTI_POLICY_ID", &cfg.policyID},
		{"SPAGHETTI_POLICY_VERSION", &cfg.policyVersion},
		{"SPAGHETTI_POLICY_SEND_PERMISSION", &cfg.permission},
		{"SPAGHETTI_AUTHZ_DB_PATH", &cfg.dbPath},
		{"SPAGHETTI_AUTHZ_ALPHAD", &cfg.alphad},
		{"SPAGHETTI_AUTHZ_CHAIN_ID", &cfg.chainID},
		{"SPAGHETTI_AUTHZ_SUBMITTER", &cfg.submitter},
		{"SPAGHETTI_AUTHZ_KEYRING_BACKEND", &cfg.keyringBackend},
		{"SPAGHETTI_AUTHZ_ISSUER_ALPHA_SEED_FILE", &cfg.issuerAlphaSeed},
		{"SPAGHETTI_AUTHZ_ISSUER_BETA_SEED_FILE", &cfg.issuerBetaSeed},
	}
	for _, value := range requiredValues {
		if *value.target, err = required(value.name); err != nil {
			return config{}, err
		}
	}
	if !filepath.IsAbs(cfg.alphad) {
		return config{}, fmt.Errorf("SPAGHETTI_AUTHZ_ALPHAD must be an absolute path")
	}
	if err := authorization.ValidateAccountAddress(cfg.submitter); err != nil {
		return config{}, fmt.Errorf("SPAGHETTI_AUTHZ_SUBMITTER must be a canonical Cosmos address: %w", err)
	}
	if strings.ContainsAny(cfg.policyID+cfg.permission, "|\r\n") {
		return config{}, fmt.Errorf("policy identity and permission must not contain descriptor delimiters")
	}
	version, err := strconv.ParseUint(cfg.policyVersion, 10, 64)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != cfg.policyVersion {
		return config{}, fmt.Errorf("SPAGHETTI_POLICY_VERSION must be a canonical positive uint64")
	}
	cfg.reconcileInterval, err = parseRequiredDuration(getenv, "SPAGHETTI_AUTHZ_RECONCILE_INTERVAL")
	if err != nil || cfg.reconcileInterval <= 0 {
		return config{}, fmt.Errorf("SPAGHETTI_AUTHZ_RECONCILE_INTERVAL must be a positive duration")
	}
	cfg.commitMaxAttempts = defaultCommitMaxAttempts
	if raw := strings.TrimSpace(getenv("SPAGHETTI_AUTHZ_COMMIT_MAX_ATTEMPTS")); raw != "" {
		cfg.commitMaxAttempts, err = strconv.Atoi(raw)
		if err != nil || cfg.commitMaxAttempts <= 0 {
			return config{}, fmt.Errorf("SPAGHETTI_AUTHZ_COMMIT_MAX_ATTEMPTS must be positive")
		}
	}
	cfg.commitPollInterval = defaultCommitPollInterval
	if raw := strings.TrimSpace(getenv("SPAGHETTI_AUTHZ_COMMIT_POLL_INTERVAL")); raw != "" {
		cfg.commitPollInterval, err = time.ParseDuration(raw)
		if err != nil || cfg.commitPollInterval < 0 {
			return config{}, fmt.Errorf("SPAGHETTI_AUTHZ_COMMIT_POLL_INTERVAL must not be negative")
		}
	}
	cfg.home = strings.TrimSpace(getenv("SPAGHETTI_AUTHZ_HOME"))
	cfg.node = strings.TrimSpace(getenv("SPAGHETTI_AUTHZ_NODE"))
	cfg.walletAttribute = strings.TrimSpace(getenv("SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE"))
	if raw := strings.TrimSpace(getenv("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP")); raw != "" {
		cfg.enableWalletLookup, err = strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP must be a boolean")
		}
	}
	return cfg, nil
}

func parseRequiredDuration(getenv func(string) string, name string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return 0, fmt.Errorf("missing required environment variable %s", name)
	}
	return time.ParseDuration(raw)
}

func (c config) keycloakConfig() remote.KeycloakConfig {
	return remote.KeycloakConfig{
		BaseURL: c.keycloakBaseURL, Realm: c.keycloakRealm,
		ClientID: c.keycloakClientID, ClientSecret: c.keycloakClientSecret,
		EnableWalletAttributeLookup: c.enableWalletLookup, WalletAttributeName: c.walletAttribute,
	}
}

func (c config) evaluator() policy.AttributeEvaluator {
	return policy.AttributeEvaluator{
		PolicyID: c.policyID, PolicyVersion: c.policyVersion,
		Operation: authorization.MsgSendTypeURL, RequiredPermission: c.permission,
	}
}

func (c config) readerConfig() authorization.AlphadAuthorizationStateReaderConfig {
	return authorization.AlphadAuthorizationStateReaderConfig{BinaryPath: c.alphad, Home: c.home, Node: c.node}
}

func (c config) publisherConfig() authorization.AlphadPublisherConfig {
	return authorization.AlphadPublisherConfig{
		BinaryPath: c.alphad, From: c.submitter, ChainID: c.chainID,
		KeyringBackend: c.keyringBackend, Home: c.home, Node: c.node,
	}
}

func (c config) confirmerConfig() authorization.AlphadCommitConfig {
	return authorization.AlphadCommitConfig{
		BinaryPath: c.alphad, Submitter: c.submitter, Home: c.home, Node: c.node,
		MaxAttempts: c.commitMaxAttempts, PollInterval: c.commitPollInterval,
	}
}

func (c config) workerConfig() authorization.AuthorizationReconciliationWorkerConfig {
	hash := authorization.KeycloakPolicyHash(c.policyID, c.policyVersion, c.permission)
	return authorization.AuthorizationReconciliationWorkerConfig{ChainID: c.chainID, PolicyHash: hash[:]}
}

func (c config) schedulerConfig() authorization.AuthorizationReconciliationSchedulerConfig {
	return authorization.AuthorizationReconciliationSchedulerConfig{Interval: c.reconcileInterval}
}
