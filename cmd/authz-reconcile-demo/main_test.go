package main

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"spaghetti/internal/authorization"
	"spaghetti/internal/remote/policy"
)

const (
	testSubject = "cosmos1fl48vsnmsdzcv85q5d2q4z5ajdha8yu34mf0eh"
	secretValue = "DO_NOT_EXPOSE_CLIENT_SECRET"
)

func validArgs() []string {
	return []string{
		"--subject", testSubject,
		"--submitter", testSubject,
		"--alphad", "/usr/local/bin/alphad",
		"--chain-id", "alpha-1",
		"--keyring-backend", "test",
		"--issuer-alpha-seed-file", "/tmp/issuer-alpha.seed",
		"--issuer-beta-seed-file", "/tmp/issuer-beta.seed",
		"--home", "/tmp/alpha-home",
		"--node", "tcp://127.0.0.1:26657",
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"SPAGHETTI_KEYCLOAK_BASE_URL":      "https://keycloak.example.test",
		"SPAGHETTI_KEYCLOAK_REALM":         "alpha",
		"SPAGHETTI_KEYCLOAK_CLIENT_ID":     "authz-middleware",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET": secretValue,
		"SPAGHETTI_POLICY_ID":              "policy-bank-send",
		"SPAGHETTI_POLICY_VERSION":         "7",
		"SPAGHETTI_POLICY_SEND_PERMISSION": "supply.send",
	}
}

func envLookup(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestRequiredFlags(t *testing.T) {
	for _, name := range []string{
		"subject", "submitter", "alphad", "chain-id", "keyring-backend",
		"issuer-alpha-seed-file", "issuer-beta-seed-file",
	} {
		t.Run(name, func(t *testing.T) {
			args := validArgs()
			for i := 0; i < len(args)-1; i += 2 {
				if args[i] == "--"+name {
					args[i+1] = ""
				}
			}
			_, err := loadCommandConfig(args, envLookup(validEnvironment()))
			if err == nil || !strings.Contains(err.Error(), "--"+name) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRequiredEnvironmentAndCanonicalVersion(t *testing.T) {
	for _, name := range []string{
		"SPAGHETTI_KEYCLOAK_BASE_URL", "SPAGHETTI_KEYCLOAK_REALM",
		"SPAGHETTI_KEYCLOAK_CLIENT_ID", "SPAGHETTI_KEYCLOAK_CLIENT_SECRET",
		"SPAGHETTI_POLICY_ID", "SPAGHETTI_POLICY_VERSION", "SPAGHETTI_POLICY_SEND_PERMISSION",
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnvironment()
			delete(env, name)
			_, err := loadCommandConfig(validArgs(), envLookup(env))
			if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), secretValue) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	env := validEnvironment()
	env["SPAGHETTI_POLICY_VERSION"] = "07"
	if _, err := loadCommandConfig(validArgs(), envLookup(env)); err == nil || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("malformed policy version error = %v", err)
	}
}

func TestRequesterControlledMetadataFlagsRejected(t *testing.T) {
	for _, name := range []string{
		"batch-id", "authorization-id", "policy-id", "policy-version", "policy-hash",
		"issuer-set-id", "receiver", "denom", "max-amount",
	} {
		t.Run(name, func(t *testing.T) {
			args := append(validArgs(), "--"+name, "requester-value")
			if _, err := loadCommandConfig(args, envLookup(validEnvironment())); err == nil {
				t.Fatalf("--%s was accepted", name)
			}
		})
	}
}

func TestCommandComposition(t *testing.T) {
	config, err := loadCommandConfig(validArgs(), envLookup(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	wantEvaluator := policy.AttributeEvaluator{
		PolicyID: "policy-bank-send", PolicyVersion: "7",
		Operation: authorization.MsgSendTypeURL, RequiredPermission: "supply.send",
	}
	if got := config.policyEvaluator(); got != wantEvaluator {
		t.Fatalf("evaluator = %+v, want %+v", got, wantEvaluator)
	}
	wantReader := authorization.AlphadAuthorizationStateReaderConfig{
		BinaryPath: "/usr/local/bin/alphad", Home: "/tmp/alpha-home", Node: "tcp://127.0.0.1:26657",
	}
	if got := config.stateReaderConfig(); got != wantReader {
		t.Fatalf("reader config = %+v, want %+v", got, wantReader)
	}
	request := config.reconcileRequest()
	if request.Subject != testSubject || request.MsgTypeURL != authorization.MsgSendTypeURL || request.ChainID != "alpha-1" {
		t.Fatalf("reconcile request = %+v", request)
	}
	wantHash := authorization.KeycloakPolicyHash("policy-bank-send", "7", "supply.send")
	if !reflect.DeepEqual(request.PolicyHash, wantHash[:]) {
		t.Fatalf("policy hash = %x, want %x", request.PolicyHash, wantHash)
	}
}

func TestOutputFields(t *testing.T) {
	tests := []struct {
		name   string
		result authorization.AuthorizationReconcileResult
		keys   []string
	}{
		{
			name: "revoked",
			result: authorization.AuthorizationReconcileResult{
				Status: authorization.ReconcileRevoked, AuthorizationID: "auth-1", BatchID: 42,
				BatchHash: sha256.Sum256([]byte("batch")), TxHash: "ABC123", Height: 88,
			},
			keys: []string{"authorization_id", "batch_hash", "batch_id", "height", "status", "tx_hash"},
		},
		{name: "noop", result: authorization.AuthorizationReconcileResult{Status: authorization.ReconcileNoopPolicyAllows}, keys: []string{"status"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(output(tt.result))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, tt.keys) {
				t.Fatalf("output keys = %v, want %v", keys, tt.keys)
			}
			text := string(encoded)
			for _, forbidden := range []string{secretValue, "supply.send", "seed", "signature", "private", "role"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("output exposed %q: %s", forbidden, text)
				}
			}
		})
	}
}
