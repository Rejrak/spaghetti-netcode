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
	testSubject  = "cosmos1fl48vsnmsdzcv85q5d2q4z5ajdha8yu34mf0eh"
	testReceiver = "cosmos1f9xjhxm0plzrh9cskf4qee4pc2xwp0n0556gh0"
	secretValue  = "DO_NOT_EXPOSE_CLIENT_SECRET"
)

func validArgs() []string {
	return []string{
		"--batch-id", "42",
		"--subject", testSubject,
		"--receiver", testReceiver,
		"--amount", "1000",
		"--submitter", testSubject,
		"--alphad", "/usr/local/bin/alphad",
		"--chain-id", "alpha-1",
		"--keyring-backend", "test",
		"--issuer-alpha-seed-file", "/tmp/issuer-alpha.seed",
		"--issuer-beta-seed-file", "/tmp/issuer-beta.seed",
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

func TestRequiredKeycloakAndPolicyEnvironment(t *testing.T) {
	required := []string{
		"SPAGHETTI_KEYCLOAK_BASE_URL",
		"SPAGHETTI_KEYCLOAK_REALM",
		"SPAGHETTI_KEYCLOAK_CLIENT_ID",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET",
		"SPAGHETTI_POLICY_ID",
		"SPAGHETTI_POLICY_VERSION",
		"SPAGHETTI_POLICY_SEND_PERMISSION",
	}
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			env := validEnvironment()
			delete(env, name)
			_, err := loadCommandConfig(validArgs(), envLookup(env))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v, want missing %s", err, name)
			}
			if strings.Contains(err.Error(), secretValue) {
				t.Fatalf("error exposed client secret: %v", err)
			}
		})
	}
}

func TestPolicyConfigurationAndTrustedProfile(t *testing.T) {
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
	request := config.issueRequest()
	if request.AuthorizationContext.IssuerSetID != 9 || request.BatchContext.IssuerSetID != 9 {
		t.Fatalf("issuer set IDs = %d/%d", request.AuthorizationContext.IssuerSetID, request.BatchContext.IssuerSetID)
	}
	if request.Facts.Denom != "token" || request.AuthorizationContext.AllowedDenom != "token" {
		t.Fatalf("denoms = %q/%q", request.Facts.Denom, request.AuthorizationContext.AllowedDenom)
	}
	if request.AuthorizationContext.MaxAmount != "5000" {
		t.Fatalf("max amount = %q", request.AuthorizationContext.MaxAmount)
	}
	if request.AuthorizationContext.AuthorizationID != "keycloak-bank-send-42" {
		t.Fatalf("authorization ID = %q", request.AuthorizationContext.AuthorizationID)
	}
	if request.AuthorizationContext.AllowedReceiver != testReceiver {
		t.Fatalf("trusted receiver = %q", request.AuthorizationContext.AllowedReceiver)
	}
	if request.BatchContext.PolicyID != "policy-bank-send" || request.BatchContext.PolicyVersion != 7 {
		t.Fatalf("trusted policy = %q/%d", request.BatchContext.PolicyID, request.BatchContext.PolicyVersion)
	}
	wantPolicyHash := configuredPolicyHash("policy-bank-send", "7", "supply.send")
	if !reflect.DeepEqual(request.BatchContext.PolicyHash, wantPolicyHash[:]) {
		t.Fatalf("trusted policy hash = %x, want %x", request.BatchContext.PolicyHash, wantPolicyHash)
	}
}

func TestAuthorizationIDChangesWithBatchID(t *testing.T) {
	config, err := loadCommandConfig(validArgs(), envLookup(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	first := config.issueRequest().AuthorizationContext.AuthorizationID
	config.options.batchID++
	second := config.issueRequest().AuthorizationContext.AuthorizationID
	if first == second || second != "keycloak-bank-send-43" {
		t.Fatalf("authorization IDs = %q/%q", first, second)
	}
}

func TestPolicyMetadataCannotComeFromCLI(t *testing.T) {
	for _, flagName := range []string{
		"--policy-id", "--policy-version", "--permission", "--issuer-set-id",
		"--denom", "--max-amount", "--policy-hash", "--authorization-id",
	} {
		t.Run(flagName, func(t *testing.T) {
			args := append(validArgs(), flagName, "requester-value")
			if _, err := loadCommandConfig(args, envLookup(validEnvironment())); err == nil {
				t.Fatalf("requester-controlled %s flag was accepted", flagName)
			}
		})
	}
	config, err := loadCommandConfig(validArgs(), envLookup(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	if config.issueRequest().BatchContext.PolicyID != validEnvironment()["SPAGHETTI_POLICY_ID"] {
		t.Fatal("batch policy did not come from environment configuration")
	}
}

func TestConfiguredPolicyHash(t *testing.T) {
	wantDescriptor := "alpha.keycloak.attribute-policy.v1" +
		"|policy_id=policy-bank-send" +
		"|policy_version=7" +
		"|operation=/cosmos.bank.v1beta1.MsgSend" +
		"|permission=supply.send" +
		"|denom=token" +
		"|max_amount=5000"
	if got := configuredPolicyDescriptor("policy-bank-send", "7", "supply.send"); got != wantDescriptor {
		t.Fatalf("descriptor = %q, want %q", got, wantDescriptor)
	}
	wantHash := sha256.Sum256([]byte(wantDescriptor))
	first := configuredPolicyHash("policy-bank-send", "7", "supply.send")
	second := configuredPolicyHash("policy-bank-send", "7", "supply.send")
	if first != wantHash || second != first {
		t.Fatalf("hash is not deterministic: %x/%x want %x", first, second, wantHash)
	}
	if changed := configuredPolicyHash("policy-bank-send-v2", "7", "supply.send"); changed == first {
		t.Fatal("policy identity change did not change policy hash")
	}
}

func TestConfigurationRejectsInvalidValues(t *testing.T) {
	t.Run("malformed policy version", func(t *testing.T) {
		env := validEnvironment()
		env["SPAGHETTI_POLICY_VERSION"] = "01"
		_, err := loadCommandConfig(validArgs(), envLookup(env))
		if err == nil || !strings.Contains(err.Error(), "SPAGHETTI_POLICY_VERSION") || strings.Contains(err.Error(), secretValue) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("zero batch ID", func(t *testing.T) {
		args := validArgs()
		args[1] = "0"
		if _, err := loadCommandConfig(args, envLookup(validEnvironment())); err == nil {
			t.Fatal("zero batch ID accepted")
		}
	})
}

func TestFactsRejectThroughExistingRecordBoundary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*commandConfig)
	}{
		{name: "subject", mutate: func(c *commandConfig) { c.options.subject = "bad-subject" }},
		{name: "receiver", mutate: func(c *commandConfig) { c.options.receiver = "bad-receiver" }},
		{name: "amount", mutate: func(c *commandConfig) { c.options.amount = "0" }},
		{name: "amount exceeds max", mutate: func(c *commandConfig) { c.options.amount = "6000" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := loadCommandConfig(validArgs(), envLookup(validEnvironment()))
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&config)
			request := config.issueRequest()
			_, err = authorization.BuildAuthorizationRecord(request.Facts, policy.PolicyDecision{
				Allow: true, ReasonCode: policy.ReasonOK,
				PolicyID: config.environment.policyID, PolicyVersion: config.environment.policyVersion,
			}, request.AuthorizationContext)
			if err == nil {
				t.Fatal("invalid facts passed existing record boundary")
			}
		})
	}
}

func TestOutputContainsOnlyPublicCorrelationFields(t *testing.T) {
	config, err := loadCommandConfig(validArgs(), envLookup(validEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	request := config.issueRequest()
	record, err := authorization.BuildAuthorizationRecord(request.Facts, policy.PolicyDecision{
		Allow: true, ReasonCode: policy.ReasonOK,
		PolicyID: config.environment.policyID, PolicyVersion: config.environment.policyVersion,
	}, request.AuthorizationContext)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config.output(authorization.AuthorizationIssueResult{
		AuthorizationRecord: record,
		BatchHash:           sha256.Sum256([]byte("batch")),
		TxHash:              "ABC123",
		Height:              88,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	gotKeys := make([]string, 0, len(fields))
	for key := range fields {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{
		"authorization_id", "batch_hash", "batch_id", "denom", "height", "max_amount",
		"policy_id", "policy_version", "receiver", "subject", "tx_hash",
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("output fields = %v, want %v", gotKeys, wantKeys)
	}
	text := string(encoded)
	for _, forbidden := range []string{secretValue, "supply.send", "issuer-alpha.seed", "issuer-beta.seed", "signature", "private"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("output exposed %q: %s", forbidden, text)
		}
	}
}
