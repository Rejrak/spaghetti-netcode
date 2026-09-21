package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spaghetti/internal/authorization"
	"spaghetti/internal/remote/policy"
)

const controlPlaneTestAddress = "cosmos1fl48vsnmsdzcv85q5d2q4z5ajdha8yu34mf0eh"

func validEnvironment() map[string]string {
	return map[string]string{
		"SPAGHETTI_KEYCLOAK_BASE_URL":             "http://127.0.0.1:18080",
		"SPAGHETTI_KEYCLOAK_REALM":                "alpha",
		"SPAGHETTI_KEYCLOAK_CLIENT_ID":            "authz-middleware",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET":        "test-client-secret",
		"SPAGHETTI_POLICY_ID":                     "policy-bank-send",
		"SPAGHETTI_POLICY_VERSION":                "7",
		"SPAGHETTI_POLICY_SEND_PERMISSION":        "supply.transaction.send",
		"SPAGHETTI_AUTHZ_DB_PATH":                 "/tmp/authz-control-plane.db",
		"SPAGHETTI_AUTHZ_ALPHAD":                  "/tmp/alphad",
		"SPAGHETTI_AUTHZ_CHAIN_ID":                "alpha-1",
		"SPAGHETTI_AUTHZ_SUBMITTER":               controlPlaneTestAddress,
		"SPAGHETTI_AUTHZ_KEYRING_BACKEND":         "test",
		"SPAGHETTI_AUTHZ_RECONCILE_INTERVAL":      "30s",
		"SPAGHETTI_AUTHZ_ISSUER_ALPHA_SEED_FILE":  "/tmp/issuer-alpha.seed",
		"SPAGHETTI_AUTHZ_ISSUER_BETA_SEED_FILE":   "/tmp/issuer-beta.seed",
		"SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP": "true",
		"SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE":     "walletAddress",
		"SPAGHETTI_AUTHZ_HOME":                    "/tmp/alpha-home",
		"SPAGHETTI_AUTHZ_NODE":                    "tcp://127.0.0.1:26657",
	}
}

func getenv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadConfigRequiresEnvironment(t *testing.T) {
	required := []string{
		"SPAGHETTI_KEYCLOAK_BASE_URL", "SPAGHETTI_KEYCLOAK_REALM", "SPAGHETTI_KEYCLOAK_CLIENT_ID",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET", "SPAGHETTI_POLICY_ID", "SPAGHETTI_POLICY_VERSION",
		"SPAGHETTI_POLICY_SEND_PERMISSION", "SPAGHETTI_AUTHZ_DB_PATH", "SPAGHETTI_AUTHZ_ALPHAD",
		"SPAGHETTI_AUTHZ_CHAIN_ID", "SPAGHETTI_AUTHZ_SUBMITTER", "SPAGHETTI_AUTHZ_KEYRING_BACKEND",
		"SPAGHETTI_AUTHZ_RECONCILE_INTERVAL", "SPAGHETTI_AUTHZ_ISSUER_ALPHA_SEED_FILE",
		"SPAGHETTI_AUTHZ_ISSUER_BETA_SEED_FILE",
	}
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			values := validEnvironment()
			delete(values, name)
			if _, err := loadConfig(getenv(values)); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadConfigValidationAndDefaults(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"noncanonical policy version", "SPAGHETTI_POLICY_VERSION", "07"},
		{"zero policy version", "SPAGHETTI_POLICY_VERSION", "0"},
		{"policy delimiter", "SPAGHETTI_POLICY_ID", "policy|other"},
		{"permission delimiter", "SPAGHETTI_POLICY_SEND_PERMISSION", "supply\npermission"},
		{"relative alphad", "SPAGHETTI_AUTHZ_ALPHAD", "alphad"},
		{"invalid submitter", "SPAGHETTI_AUTHZ_SUBMITTER", "cosmos-malformed"},
		{"zero interval", "SPAGHETTI_AUTHZ_RECONCILE_INTERVAL", "0s"},
		{"negative interval", "SPAGHETTI_AUTHZ_RECONCILE_INTERVAL", "-1s"},
		{"malformed interval", "SPAGHETTI_AUTHZ_RECONCILE_INTERVAL", "later"},
		{"zero attempts", "SPAGHETTI_AUTHZ_COMMIT_MAX_ATTEMPTS", "0"},
		{"malformed attempts", "SPAGHETTI_AUTHZ_COMMIT_MAX_ATTEMPTS", "many"},
		{"negative poll", "SPAGHETTI_AUTHZ_COMMIT_POLL_INTERVAL", "-1s"},
		{"malformed poll", "SPAGHETTI_AUTHZ_COMMIT_POLL_INTERVAL", "soon"},
		{"malformed wallet toggle", "SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP", "maybe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := validEnvironment()
			values[tt.key] = tt.value
			if _, err := loadConfig(getenv(values)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	values := validEnvironment()
	cfg, err := loadConfig(getenv(values))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.commitMaxAttempts != defaultCommitMaxAttempts || cfg.commitPollInterval != defaultCommitPollInterval {
		t.Fatalf("retry defaults = %d/%v", cfg.commitMaxAttempts, cfg.commitPollInterval)
	}
}

func TestConfigBuildsExactComponentConfiguration(t *testing.T) {
	values := validEnvironment()
	values["SPAGHETTI_AUTHZ_COMMIT_MAX_ATTEMPTS"] = "9"
	values["SPAGHETTI_AUTHZ_COMMIT_POLL_INTERVAL"] = "250ms"
	cfg, err := loadConfig(getenv(values))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.evaluator(), (policy.AttributeEvaluator{
		PolicyID: "policy-bank-send", PolicyVersion: "7", Operation: authorization.MsgSendTypeURL,
		RequiredPermission: "supply.transaction.send",
	}); got != want {
		t.Fatalf("evaluator = %+v, want %+v", got, want)
	}
	reader := cfg.readerConfig()
	if reader.BinaryPath != "/tmp/alphad" || reader.Home != "/tmp/alpha-home" || reader.Node != "tcp://127.0.0.1:26657" {
		t.Fatalf("reader config = %+v", reader)
	}
	publisher := cfg.publisherConfig()
	if publisher.BinaryPath != reader.BinaryPath || publisher.From != controlPlaneTestAddress || publisher.ChainID != "alpha-1" ||
		publisher.KeyringBackend != "test" || publisher.Home != reader.Home || publisher.Node != reader.Node {
		t.Fatalf("publisher config = %+v", publisher)
	}
	confirmer := cfg.confirmerConfig()
	if confirmer.BinaryPath != reader.BinaryPath || confirmer.Submitter != controlPlaneTestAddress || confirmer.Home != reader.Home ||
		confirmer.Node != reader.Node || confirmer.MaxAttempts != 9 || confirmer.PollInterval != 250*time.Millisecond {
		t.Fatalf("confirmer config = %+v", confirmer)
	}
	wantHash := authorization.KeycloakPolicyHash("policy-bank-send", "7", "supply.transaction.send")
	worker := cfg.workerConfig()
	if worker.ChainID != "alpha-1" || !bytes.Equal(worker.PolicyHash, wantHash[:]) {
		t.Fatalf("worker config = %+v", worker)
	}
	if cfg.schedulerConfig().Interval != 30*time.Second {
		t.Fatalf("scheduler config = %+v", cfg.schedulerConfig())
	}
	keycloak := cfg.keycloakConfig()
	if keycloak.BaseURL != values["SPAGHETTI_KEYCLOAK_BASE_URL"] || keycloak.Realm != "alpha" ||
		!keycloak.EnableWalletAttributeLookup || keycloak.WalletAttributeName != "walletAddress" {
		t.Fatalf("Keycloak config mismatch")
	}
}

func TestBuildSchedulerComposesExistingComponents(t *testing.T) {
	cfg, err := loadConfig(getenv(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.issuerAlphaSeed = filepath.Join(dir, "alpha.seed")
	cfg.issuerBetaSeed = filepath.Join(dir, "beta.seed")
	if err := os.WriteFile(cfg.issuerAlphaSeed, []byte(strings.Repeat("00", ed25519.SeedSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.issuerBetaSeed, []byte(strings.Repeat("11", ed25519.SeedSize)), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduler, err := buildScheduler(cfg, &fakeRepo{}, nil)
	if err != nil || scheduler == nil {
		t.Fatalf("scheduler/error = %v/%v", scheduler, err)
	}
}

type fakeRepo struct {
	closed   bool
	closeErr error
}

func (*fakeRepo) EnsureManagedSubject(context.Context, string) error    { return nil }
func (*fakeRepo) ListManagedSubjects(context.Context) ([]string, error) { return nil, nil }
func (r *fakeRepo) Close() error                                        { r.closed = true; return r.closeErr }

type fakeScheduler struct {
	calls int
	err   error
}

func (s *fakeScheduler) Run(context.Context) error { s.calls++; return s.err }

func TestRunLifecycleAndClose(t *testing.T) {
	cfg, err := loadConfig(getenv(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("cancelled is clean", func(t *testing.T) {
		repo := &fakeRepo{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		scheduler := &fakeScheduler{err: context.Canceled}
		var logs bytes.Buffer
		err := runWith(ctx, cfg, slog.New(slog.NewJSONHandler(&logs, nil)),
			func(string) (controlPlaneRepo, error) { return repo, nil },
			func(config, authorization.ManagedSubjectStore, *slog.Logger) (controlPlaneScheduler, error) {
				return scheduler, nil
			},
		)
		if err != nil || scheduler.calls != 1 || !repo.closed {
			t.Fatalf("error/calls/closed = %v/%d/%v", err, scheduler.calls, repo.closed)
		}
		if strings.Count(logs.String(), `"msg":"authz_control_plane_started"`) != 1 ||
			strings.Count(logs.String(), `"msg":"authz_control_plane_stopped"`) != 1 {
			t.Fatalf("lifecycle logs = %s", logs.String())
		}
	})
	t.Run("startup failure closes database", func(t *testing.T) {
		repo := &fakeRepo{}
		want := errors.New("invalid seed")
		err := runWith(context.Background(), cfg, nil,
			func(string) (controlPlaneRepo, error) { return repo, nil },
			func(config, authorization.ManagedSubjectStore, *slog.Logger) (controlPlaneScheduler, error) {
				return nil, want
			},
		)
		if !errors.Is(err, want) || !repo.closed {
			t.Fatalf("error/closed = %v/%v", err, repo.closed)
		}
	})
	t.Run("runtime and close failures propagate", func(t *testing.T) {
		repo := &fakeRepo{closeErr: errors.New("close failed")}
		runFailure := errors.New("scheduler failed")
		err := runWith(context.Background(), cfg, nil,
			func(string) (controlPlaneRepo, error) { return repo, nil },
			func(config, authorization.ManagedSubjectStore, *slog.Logger) (controlPlaneScheduler, error) {
				return &fakeScheduler{err: runFailure}, nil
			},
		)
		if !errors.Is(err, runFailure) || !strings.Contains(err.Error(), "close failed") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestConfigFormattingAndErrorsDoNotExposeSecrets(t *testing.T) {
	values := validEnvironment()
	secret := "never-print-client-secret"
	seed := "/never-print-seed-path"
	values["SPAGHETTI_KEYCLOAK_CLIENT_SECRET"] = secret
	values["SPAGHETTI_AUTHZ_ISSUER_ALPHA_SEED_FILE"] = seed
	cfg, err := loadConfig(getenv(values))
	if err != nil {
		t.Fatal(err)
	}
	formatted := fmt.Sprintf("%+v", cfg)
	if strings.Contains(formatted, secret) || strings.Contains(formatted, seed) {
		t.Fatalf("config formatting exposed secret material: %s", formatted)
	}
	values["SPAGHETTI_POLICY_VERSION"] = "bad"
	_, err = loadConfig(getenv(values))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), seed) {
		t.Fatalf("validation error exposed secret material: %v", err)
	}
	cfg.issuerAlphaSeed = seed
	_, err = loadSigners(cfg)
	if err == nil || strings.Contains(err.Error(), seed) {
		t.Fatalf("seed-loading error exposed path: %v", err)
	}
}

func TestWorkerConfigReturnsDetachedPolicyHash(t *testing.T) {
	cfg, err := loadConfig(getenv(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	first := cfg.workerConfig()
	second := cfg.workerConfig()
	first.PolicyHash[0] ^= 0xff
	if reflect.DeepEqual(first.PolicyHash, second.PolicyHash) {
		t.Fatal("worker policy hash aliases shared state")
	}
}
