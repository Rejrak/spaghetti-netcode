package server

import (
	"encoding/json"
	"strings"
	"testing"
)

var requiredConfigEnv = []string{
	"SPAGHETTI_DB_PATH",
	"SPAGHETTI_KEYCLOAK_BASE_URL",
	"SPAGHETTI_KEYCLOAK_REALM",
	"SPAGHETTI_KEYCLOAK_CLIENT_ID",
	"SPAGHETTI_KEYCLOAK_CLIENT_SECRET",
	"SPAGHETTI_POLICY_ID",
	"SPAGHETTI_POLICY_VERSION",
	"SPAGHETTI_POLICY_SEND_PERMISSION",
	"SPAGHETTI_POLICY_MAX_ATTRIBUTE_AGE",
}

func TestLoadRuntimeConfigRequiresExplicitValues(t *testing.T) {
	for _, missing := range requiredConfigEnv {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP", "")
			for _, name := range requiredConfigEnv {
				value := "configured"
				if name == "SPAGHETTI_POLICY_MAX_ATTRIBUTE_AGE" {
					value = "1m"
				}
				t.Setenv(name, value)
			}
			t.Setenv(missing, "")
			_, err := LoadRuntimeConfigFromEnv()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("expected missing %s error, got %v", missing, err)
			}
		})
	}
}

func TestLoadRuntimeConfigKeepsSecretOutOfGossipConfig(t *testing.T) {
	t.Setenv("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP", "")
	values := map[string]string{
		"SPAGHETTI_DB_PATH":                  "/tmp/middleware.db",
		"SPAGHETTI_KEYCLOAK_BASE_URL":        "https://identity.example",
		"SPAGHETTI_KEYCLOAK_REALM":           "example",
		"SPAGHETTI_KEYCLOAK_CLIENT_ID":       "middleware",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET":   "test-secret-from-environment",
		"SPAGHETTI_POLICY_ID":                "bank-send-policy",
		"SPAGHETTI_POLICY_VERSION":           "1",
		"SPAGHETTI_POLICY_SEND_PERMISSION":   "supply.transaction.send",
		"SPAGHETTI_POLICY_MAX_ATTRIBUTE_AGE": "1m",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}

	cfg, err := LoadRuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeycloakClientSecret != values["SPAGHETTI_KEYCLOAK_CLIENT_SECRET"] {
		t.Fatal("secret was not loaded from the environment")
	}
	payload, err := json.Marshal(cfg.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), cfg.KeycloakClientSecret) {
		t.Fatal("secret leaked into gossipable cluster config")
	}
}
