package configcluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type Version uint64

type NodeID string

type ClusterConfig struct {
	// Parametri attualmente hardcoded in server_actors.go
	DBPath string

	// Keycloak
	KeycloakBaseURL             string
	KeycloakRealm               string
	KeycloakClientID            string
	KeycloakWalletAttributeName string
	KeycloakEnableWalletLookup  bool

	// Dynamic attributes backend
	AttributesBaseURL string

	// (eventuale) Cosmos LCD URL, ecc. in futuro
	// CosmosLCDBaseURL string
}

type ConfigSnapshot struct {
	Version    Version
	Data       ClusterConfig
	Hash       string // opzionale per debug/integrita
	Signatures map[NodeID]string // Mappa NodeID -> Firma dell'hash per il quorum
}

type VersionStamp struct {
	Version Version
	NodeID  NodeID
}

func (v VersionStamp) IsNewerThan(other VersionStamp) bool {
	if v.Version != other.Version {
		return v.Version > other.Version
	}
	return string(v.NodeID) > string(other.NodeID)
}

func (s ConfigSnapshot) ComputeHash() string {
	payload, err := json.Marshal(s.Data)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
