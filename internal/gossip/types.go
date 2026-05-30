package gossip

type ConfigVersion struct {
	NodeID  string `json:"node_id"`
	Version uint64 `json:"version"`
}

type ConfigVersionResponse struct {
	Version    uint64 `json:"version"`
	Hash       string `json:"hash"`
	LastWriter string `json:"last_writer"`
}

type GetConfigRequest struct {
	SinceVersion uint64 `json:"since_version"`
}

type ConfigSnapshot struct {
	NodeID     string `json:"node_id"`
	Version    uint64 `json:"version"`
	ConfigJSON []byte            `json:"config_json"`
	Hash       string            `json:"hash"`
	Signatures map[string]string `json:"signatures"`
}

type Ack struct {
	Applied bool `json:"applied"`
}
