package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"spaghetti/internal/authorization"
)

func demoArgs() []string {
	return []string{
		"--subject", "cosmos1duzpxku5atm98qk6ywvgdjzn50yzv90q7c3r44",
		"--receiver", "cosmos1ssevndlg997a89wpw2wv9xj2vahhqcyt0gml84",
		"--amount", "1000", "--account", "alice", "--alphad", "/tmp/alphad",
		"--chain-id", "alpha-1", "--keyring-backend", "test", "--home", "/tmp/alpha-home",
		"--issuer-alpha-seed-file", "/tmp/no-alpha-seed", "--issuer-beta-seed-file", "/tmp/no-beta-seed",
	}
}

func demoEnv(name string) string {
	return map[string]string{
		"SPAGHETTI_KEYCLOAK_BASE_URL":      "http://127.0.0.1:18080",
		"SPAGHETTI_KEYCLOAK_REALM":         "alpha",
		"SPAGHETTI_KEYCLOAK_CLIENT_ID":     "authz-middleware",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET": "test-secret-never-output",
		"SPAGHETTI_POLICY_ID":              "policy-bank-send",
		"SPAGHETTI_POLICY_VERSION":         "2",
		"SPAGHETTI_POLICY_SEND_PERMISSION": "supply.transaction.send",
	}[name]
}

func TestV2DemoConfiguration(t *testing.T) {
	if _, err := parseOptions(demoArgs()); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions(append(demoArgs(), "--batch-id", "1")); err == nil {
		t.Fatal("accepted V1 batch flag")
	}
	if _, err := parseOptions(append(demoArgs(), "--policy-id", "attacker")); err == nil {
		t.Fatal("accepted requester policy metadata")
	}
	if _, err := parseOptions(append(demoArgs(), "--subject", "bad")); err == nil {
		t.Fatal("accepted malformed subject")
	}
	config, err := loadPolicy(demoEnv)
	if err != nil {
		t.Fatal(err)
	}
	hash := demoPolicyHash(config)
	if hash == authorization.KeycloakPolicyHash(config.policyID, config.policyVersion, config.permission) {
		t.Fatal("V2 policy descriptor reused V1 grant hash")
	}
	if hash != demoPolicyHash(config) {
		t.Fatal("V2 descriptor hash changed between calls")
	}
	changed := config
	changed.permission = "other"
	if hash == demoPolicyHash(changed) {
		t.Fatal("policy permission not bound by V2 hash")
	}
	if _, err := loadPolicy(func(name string) string {
		if name == "SPAGHETTI_POLICY_VERSION" {
			return "02"
		}
		return demoEnv(name)
	}); err == nil {
		t.Fatal("accepted noncanonical policy version")
	}
	if _, err := loadPolicy(func(name string) string {
		if name == "SPAGHETTI_KEYCLOAK_CLIENT_SECRET" {
			return ""
		}
		return demoEnv(name)
	}); err == nil || strings.Contains(err.Error(), "test-secret-never-output") {
		t.Fatal("missing secret not rejected safely")
	}
	var output bytes.Buffer
	err = run(context.Background(), demoArgs(), &output, demoEnv)
	if err == nil || strings.Contains(err.Error(), "test-secret-never-output") || strings.Contains(output.String(), "test-secret-never-output") {
		t.Fatal("normal validation path exposed a secret or reached live infrastructure")
	}
}
