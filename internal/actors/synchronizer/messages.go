package synchronizer

import "time"

type RegisterAddress struct {
	Address string
	Session string
}

type ForceSync struct{} // chiedi uno sync immediato

type Tick struct{} // tick interno

type Config struct {
	DBPath string

	PollInterval time.Duration
	StaleAfter   time.Duration
	MaxBatch     int

	// timeout per le chiamate remote
	RemoteTimeout time.Duration

	KeycloakBaseURL             string // es: https://keycloak.example.com
	KeycloakRealm               string // es: myrealm
	KeycloakClientSecret        string // es: supersecret
	KeycloakClientID            string // es: spaghetti-service
	KeycloakWalletAttributeName string // opzionale, default: "walletAddress"
	KeycloakEnableWalletLookup  bool   // se true, cerca utente anche per attributo wallet
}
