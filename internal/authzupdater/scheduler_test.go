package authzupdater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"spaghetti/internal/pkg/constraints"
	"spaghetti/internal/user"
	"testing"
	"time"
	"encoding/hex"
)

func TestConstraintHashing(t *testing.T) {
	c := constraints.CanonicalConstraints{
		MsgType:   "/cosmos.bank.v1beta1.MsgSend",
		MaxAmount: "1000000",
	}
	hash := constraints.ComputeHash(c)
	expectedHex := hex.EncodeToString(hash)
	if expectedHex == "" {
		t.Errorf("Expected valid hash, got empty string")
	}
}

func TestBuildRecordFromMockKeycloak(t *testing.T) {
	attrs := &user.Attributes{
		Roles: []string{"office_manager"},
	}
	
	rec := BuildRecord("cosmos1wallet", "/cosmos.bank.v1beta1.MsgSend", attrs, 1, 1, 1*time.Hour)
	if rec.Address != "cosmos1wallet" {
		t.Errorf("Expected address cosmos1wallet, got %s", rec.Address)
	}
	if rec.Revoked {
		t.Errorf("Expected not revoked")
	}
}

func TestBatchScheduler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"txhash": "MOCK_HASH"})
	}))
	defer server.Close()

	config := CosmosConfig{
		ChainID:     "mock-chain",
		LCDEndpoint: server.URL,
		Broadcast:   "sync",
	}
	
	scheduler := NewBatchScheduler(config, 2, 100*time.Millisecond)
	scheduler.Start()
	defer scheduler.Stop()
	
	attrs := &user.Attributes{Roles: []string{"office_manager"}}
	rec1 := BuildRecord("addr1", "msg1", attrs, 1, 1, 1*time.Hour)
	rec2 := BuildRecord("addr2", "msg2", attrs, 1, 1, 1*time.Hour)
	
	scheduler.AddRecord(rec1)
	scheduler.AddRecord(rec2) // Should trigger flush
	
	time.Sleep(50 * time.Millisecond) // Give time for flush broadcast to finish
}

func TestUrgentRevoke(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := CosmosConfig{
		LCDEndpoint: server.URL,
	}
	scheduler := NewBatchScheduler(config, 10, 10*time.Second)
	
	scheduler.RevokeUrgent("addr1", "msg1", 1, 1)
}
