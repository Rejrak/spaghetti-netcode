package authzupdater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type CosmosConfig struct {
	ChainID      string
	GRPCEndpoint string
	RPCEndpoint  string
	LCDEndpoint  string
	Signer       string // Mnemonic or Key name
	GasPrice     string
	Broadcast    string // block, sync, async
}

type BatchScheduler struct {
	config       CosmosConfig
	records      []*AuthzRecord
	batchSize    int
	interval     time.Duration
	mu           sync.Mutex
	stopCh       chan struct{}
}

func NewBatchScheduler(config CosmosConfig, batchSize int, interval time.Duration) *BatchScheduler {
	return &BatchScheduler{
		config:    config,
		records:   make([]*AuthzRecord, 0),
		batchSize: batchSize,
		interval:  interval,
		stopCh:    make(chan struct{}),
	}
}

func (s *BatchScheduler) Start() {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.flush()
			case <-s.stopCh:
				s.flush()
				return
			}
		}
	}()
}

func (s *BatchScheduler) Stop() {
	close(s.stopCh)
}

func (s *BatchScheduler) AddRecord(rec *AuthzRecord) {
	s.mu.Lock()
	s.records = append(s.records, rec)
	count := len(s.records)
	s.mu.Unlock()

	if count >= s.batchSize {
		s.flush()
	}
}

func (s *BatchScheduler) RevokeUrgent(address string, msgType string, policyVersion uint64, issuerSetId uint64) {
	// Revoke is urgent, so we broadcast immediately bypassing batching
	rec := &AuthzRecord{
		Address:         address,
		MsgType:         msgType,
		ConstraintsHash: "",
		ValidUntil:      time.Now().Unix(),
		PolicyVersion:   policyVersion,
		IssuerSetId:     issuerSetId,
		Revoked:         true,
	}

	err := s.broadcastRecords([]*AuthzRecord{rec})
	if err != nil {
		slog.Error("Failed to broadcast urgent revoke", "err", err)
	} else {
		slog.Info("Successfully broadcast urgent revoke", "address", address, "msgType", msgType)
	}
}

func (s *BatchScheduler) flush() {
	s.mu.Lock()
	if len(s.records) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.records
	s.records = make([]*AuthzRecord, 0)
	s.mu.Unlock()

	err := s.broadcastRecords(batch)
	if err != nil {
		slog.Error("Failed to broadcast batch", "err", err)
	} else {
		slog.Info("Successfully broadcast batch", "size", len(batch))
	}
}

// ComputeBatchHash computes the hash for a batch, used for signatures (Phase 6)
func ComputeBatchHash(records []*AuthzRecord, chainID string, policyVersion uint64, issuerSetId uint64) []byte {
	payload := map[string]interface{}{
		"records":        records,
		"chain_id":       chainID,
		"policy_version": policyVersion,
		"issuer_set_id":  issuerSetId,
	}
	b, _ := json.Marshal(payload)
	h := sha256.Sum256(b)
	return h[:]
}

func (s *BatchScheduler) broadcastRecords(records []*AuthzRecord) error {
	// Mock Cosmos LCD REST broadcast since we don't import Cosmos SDK grpc client in this service yet.
	slog.Info("Mocking broadcast of MsgBatchUpsertAuthorizations", "records", len(records), "lcd", s.config.LCDEndpoint)
	
	payload := map[string]interface{}{
		"tx": map[string]interface{}{
			"msg": []map[string]interface{}{
				{
					"type": "authzattrs/MsgBatchUpsertAuthorizations",
					"value": map[string]interface{}{
						"records": records,
						"signatures": []string{"mock_signature"}, 
					},
				},
			},
		},
		"mode": s.config.Broadcast,
	}
	
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	
	if s.config.LCDEndpoint == "" {
		slog.Warn("LCDEndpoint is empty, skipping actual HTTP broadcast")
		return nil
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	url := fmt.Sprintf("%s/cosmos/tx/v1beta1/txs", s.config.LCDEndpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode >= 400 {
		return fmt.Errorf("broadcast failed with status: %d", resp.StatusCode)
	}
	
	return nil
}
