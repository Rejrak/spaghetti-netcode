package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"spaghetti/internal/authorization"
	remote "spaghetti/internal/remote/keycloak"
	"spaghetti/internal/remote/policy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	required := func(name string) (string, error) {
		value := os.Getenv(name)
		if value == "" {
			return "", fmt.Errorf("missing %s", name)
		}
		return value, nil
	}
	get := func(name string) string { value, _ := required(name); return value }
	for _, name := range []string{"SPAGHETTI_LISTEN_ADDR", "SPAGHETTI_TLS_CERT_FILE", "SPAGHETTI_TLS_KEY_FILE",
		"SPAGHETTI_KEYCLOAK_BASE_URL", "SPAGHETTI_KEYCLOAK_REALM", "SPAGHETTI_KEYCLOAK_CLIENT_ID",
		"SPAGHETTI_KEYCLOAK_CLIENT_SECRET", "SPAGHETTI_KEYCLOAK_AUDIENCE", "SPAGHETTI_POLICY_ID",
		"SPAGHETTI_POLICY_VERSION", "SPAGHETTI_POLICY_HASH", "SPAGHETTI_POLICY_SEND_PERMISSION",
		"SPAGHETTI_CHAIN_ID", "SPAGHETTI_ISSUER_SET_ID", "SPAGHETTI_ISSUER_IDS",
		"SPAGHETTI_ISSUER_SEED_FILES", "SPAGHETTI_ALPHAD_PATH"} {
		if _, err := required(name); err != nil {
			return err
		}
	}
	version, err := strconv.ParseUint(get("SPAGHETTI_POLICY_VERSION"), 10, 64)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != get("SPAGHETTI_POLICY_VERSION") {
		return fmt.Errorf("invalid policy version")
	}
	setID, err := strconv.ParseUint(get("SPAGHETTI_ISSUER_SET_ID"), 10, 64)
	if err != nil || setID == 0 || strconv.FormatUint(setID, 10) != get("SPAGHETTI_ISSUER_SET_ID") {
		return fmt.Errorf("invalid issuer set ID")
	}
	policyHash, err := hex.DecodeString(get("SPAGHETTI_POLICY_HASH"))
	if err != nil || len(policyHash) != 32 {
		return fmt.Errorf("invalid policy hash")
	}
	ids := strings.Split(get("SPAGHETTI_ISSUER_IDS"), ",")
	paths := strings.Split(get("SPAGHETTI_ISSUER_SEED_FILES"), ",")
	if len(ids) != len(paths) || len(ids) == 0 || len(ids) > 16 {
		return fmt.Errorf("invalid issuer signer configuration")
	}
	signers := make([]authorization.CertificateSignerV2, 0, len(ids))
	for i, id := range ids {
		seed, err := authorization.LoadDemoIssuerSeed(paths[i])
		if err != nil {
			return fmt.Errorf("load issuer seed: %w", err)
		}
		privateKey := ed25519.NewKeyFromSeed(seed)
		clear(seed)
		signer, err := authorization.NewEd25519BatchSigner(id, privateKey)
		clear(privateKey)
		if err != nil {
			return err
		}
		signers = append(signers, signer)
	}
	keycloak := remote.NewKeycloakClient(remote.KeycloakConfig{
		BaseURL: get("SPAGHETTI_KEYCLOAK_BASE_URL"), Realm: get("SPAGHETTI_KEYCLOAK_REALM"),
		ClientID: get("SPAGHETTI_KEYCLOAK_CLIENT_ID"), ClientSecret: get("SPAGHETTI_KEYCLOAK_CLIENT_SECRET"),
		AccessTokenAudience:         get("SPAGHETTI_KEYCLOAK_AUDIENCE"),
		EnableWalletAttributeLookup: get("SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE") == "1",
	})
	state, err := authorization.NewAlphadAccountStateProviderV2(authorization.AlphadAuthorizationStateReaderConfig{
		BinaryPath: get("SPAGHETTI_ALPHAD_PATH"), Home: get("SPAGHETTI_ALPHA_HOME"), Node: get("SPAGHETTI_ALPHA_NODE"),
	}, nil)
	if err != nil {
		return err
	}
	issuer, err := authorization.NewCertificateIssuerV2(keycloak, policy.AttributeEvaluator{
		PolicyID: get("SPAGHETTI_POLICY_ID"), PolicyVersion: get("SPAGHETTI_POLICY_VERSION"),
		Operation: authorization.MsgSendTypeURL, RequiredPermission: get("SPAGHETTI_POLICY_SEND_PERMISSION"),
	}, state, signers, authorization.CertificateIssuerV2Config{
		ChainID: get("SPAGHETTI_CHAIN_ID"), PolicyID: get("SPAGHETTI_POLICY_ID"), PolicyVersion: version,
		PolicyHash: policyHash, IssuerSetID: setID, LifetimeBlocks: 40,
	}, nil)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v2/certificates", authorization.NewCertificateHTTPHandler(issuer, keycloak))
	server := &http.Server{Addr: get("SPAGHETTI_LISTEN_ADDR"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return server.ListenAndServeTLS(get("SPAGHETTI_TLS_CERT_FILE"), get("SPAGHETTI_TLS_KEY_FILE"))
}
