package server

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"spaghetti/internal/configcluster"
)

type RuntimeConfig struct {
	Cluster              configcluster.ClusterConfig
	KeycloakClientSecret string
	PolicyID             string
	PolicyVersion        string
	SendPermission       string
	MaxAttributeAge      time.Duration
}

func LoadRuntimeConfigFromEnv() (RuntimeConfig, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return "", fmt.Errorf("missing required environment variable %s", name)
		}
		return value, nil
	}

	var cfg RuntimeConfig
	var err error
	if cfg.Cluster.DBPath, err = required("SPAGHETTI_DB_PATH"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.Cluster.KeycloakBaseURL, err = required("SPAGHETTI_KEYCLOAK_BASE_URL"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.Cluster.KeycloakRealm, err = required("SPAGHETTI_KEYCLOAK_REALM"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.Cluster.KeycloakClientID, err = required("SPAGHETTI_KEYCLOAK_CLIENT_ID"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.KeycloakClientSecret, err = required("SPAGHETTI_KEYCLOAK_CLIENT_SECRET"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.PolicyID, err = required("SPAGHETTI_POLICY_ID"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.PolicyVersion, err = required("SPAGHETTI_POLICY_VERSION"); err != nil {
		return RuntimeConfig{}, err
	}
	if cfg.SendPermission, err = required("SPAGHETTI_POLICY_SEND_PERMISSION"); err != nil {
		return RuntimeConfig{}, err
	}
	maxAge, err := required("SPAGHETTI_POLICY_MAX_ATTRIBUTE_AGE")
	if err != nil {
		return RuntimeConfig{}, err
	}
	cfg.MaxAttributeAge, err = time.ParseDuration(maxAge)
	if err != nil || cfg.MaxAttributeAge <= 0 {
		return RuntimeConfig{}, errors.New("SPAGHETTI_POLICY_MAX_ATTRIBUTE_AGE must be a positive duration")
	}

	cfg.Cluster.KeycloakWalletAttributeName = strings.TrimSpace(os.Getenv("SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE"))
	cfg.Cluster.AttributesBaseURL = strings.TrimSpace(os.Getenv("SPAGHETTI_ATTRIBUTES_BASE_URL"))
	if raw := strings.TrimSpace(os.Getenv("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP")); raw != "" {
		cfg.Cluster.KeycloakEnableWalletLookup, err = strconv.ParseBool(raw)
		if err != nil {
			return RuntimeConfig{}, errors.New("SPAGHETTI_KEYCLOAK_ENABLE_WALLET_LOOKUP must be a boolean")
		}
	}
	return cfg, nil
}
