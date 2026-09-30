package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	rpctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

type fakeV2RPC struct {
	broadcastResult *rpctypes.ResultBroadcastTx
	broadcastErr    error
	txResults       []*rpctypes.ResultTx
	txErrors        []error
	broadcastCalls  int
	txCalls         int
	broadcastBytes  []byte
	queriedHash     []byte
	queryProve      bool
}

func (f *fakeV2RPC) BroadcastTxSync(_ context.Context, tx cmttypes.Tx) (*rpctypes.ResultBroadcastTx, error) {
	f.broadcastCalls++
	f.broadcastBytes = append([]byte(nil), tx...)
	return f.broadcastResult, f.broadcastErr
}

func (f *fakeV2RPC) Tx(_ context.Context, hash []byte, prove bool) (*rpctypes.ResultTx, error) {
	i := f.txCalls
	f.txCalls++
	f.queriedHash = append([]byte(nil), hash...)
	f.queryProve = prove
	if i < len(f.txErrors) && f.txErrors[i] != nil {
		return nil, f.txErrors[i]
	}
	if i < len(f.txResults) {
		return f.txResults[i], nil
	}
	return nil, fmt.Errorf("tx (%X) not found", []byte{1})
}

func TestV2TransportBroadcastAndConfirm(t *testing.T) {
	tx := []byte("signed Cosmos TxRaw fixture")
	hash := sha256.Sum256(tx)
	rpc := &fakeV2RPC{
		broadcastResult: &rpctypes.ResultBroadcastTx{Hash: hash[:]},
		txErrors:        []error{fmt.Errorf("tx (%X) not found", hash[:]), nil},
		txResults: []*rpctypes.ResultTx{nil, {
			Hash: hash[:], Height: 42, Tx: cmttypes.Tx(tx), TxResult: abci.ExecTxResult{},
		}},
	}
	broadcaster, err := NewCometV2TxBroadcaster(rpc)
	if err != nil {
		t.Fatal(err)
	}
	result, err := broadcaster.Broadcast(context.Background(), tx)
	if err != nil || result.TxHash != strings.ToUpper(hex.EncodeToString(hash[:])) ||
		rpc.broadcastCalls != 1 || !bytes.Equal(rpc.broadcastBytes, tx) {
		t.Fatal("broadcast did not send exact bytes once", err)
	}
	confirmer, err := NewCometV2TxConfirmer(rpc, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	included, err := confirmer.WaitForInclusion(context.Background(), result.TxHash)
	if err != nil || included.TxHash != result.TxHash || included.Height != 42 || included.Code != 0 ||
		rpc.txCalls != 2 || !bytes.Equal(rpc.queriedHash, hash[:]) || rpc.queryProve {
		t.Fatal("did not confirm the exact included transaction", err)
	}
}

func TestV2TransportRejectsFailures(t *testing.T) {
	tx := []byte("signed bytes")
	hash := sha256.Sum256(tx)
	hashText := hex.EncodeToString(hash[:])
	for _, tc := range []struct {
		name string
		rpc  *fakeV2RPC
		tx   []byte
	}{
		{"empty tx", &fakeV2RPC{}, nil},
		{"RPC failure", &fakeV2RPC{broadcastErr: errors.New("node unavailable")}, tx},
		{"empty response", &fakeV2RPC{}, tx},
		{"CheckTx failure", &fakeV2RPC{broadcastResult: &rpctypes.ResultBroadcastTx{Hash: hash[:], Code: 17, Codespace: "auth"}}, tx},
		{"hash mismatch", &fakeV2RPC{broadcastResult: &rpctypes.ResultBroadcastTx{Hash: bytes.Repeat([]byte{1}, 32)}}, tx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := NewCometV2TxBroadcaster(tc.rpc)
			if _, err := b.Broadcast(context.Background(), tc.tx); err == nil {
				t.Fatal("accepted failed broadcast")
			}
			if tc.name == "empty tx" && tc.rpc.broadcastCalls != 0 {
				t.Fatal("sent empty transaction")
			}
		})
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	rpc := &fakeV2RPC{}
	b, _ := NewCometV2TxBroadcaster(rpc)
	if _, err := b.Broadcast(cancelled, tx); !errors.Is(err, context.Canceled) || rpc.broadcastCalls != 0 {
		t.Fatal("cancelled broadcast reached RPC")
	}
	for _, tc := range []struct {
		name string
		rpc  *fakeV2RPC
	}{
		{"not found exhaustion", &fakeV2RPC{txErrors: []error{fmt.Errorf("tx (%X) not found", hash[:])}}},
		{"fatal query", &fakeV2RPC{txErrors: []error{errors.New("invalid node address")}}},
		{"missing response", &fakeV2RPC{txResults: []*rpctypes.ResultTx{nil}}},
		{"wrong hash", &fakeV2RPC{txResults: []*rpctypes.ResultTx{{Hash: bytes.Repeat([]byte{2}, 32), Height: 10, Tx: cmttypes.Tx(tx)}}}},
		{"zero height", &fakeV2RPC{txResults: []*rpctypes.ResultTx{{Hash: hash[:], Tx: cmttypes.Tx(tx)}}}},
		{"wrong tx bytes", &fakeV2RPC{txResults: []*rpctypes.ResultTx{{Hash: hash[:], Height: 10, Tx: cmttypes.Tx("tampered")}}}},
		{"included failure", &fakeV2RPC{txResults: []*rpctypes.ResultTx{{Hash: hash[:], Height: 10, Tx: cmttypes.Tx(tx), TxResult: abci.ExecTxResult{Code: 8, Codespace: "auth"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := NewCometV2TxConfirmer(tc.rpc, 1, 0)
			got, err := c.WaitForInclusion(context.Background(), hashText)
			if err == nil || tc.rpc.txCalls != 1 {
				t.Fatal("accepted failed inclusion")
			}
			if tc.name == "included failure" && (got.Code != 8 || !strings.Contains(err.Error(), "code=8")) {
				t.Fatal("nonzero inclusion code lost")
			}
		})
	}
	c, _ := NewCometV2TxConfirmer(rpc, 1, 0)
	if _, err := c.WaitForInclusion(context.Background(), "bad-hash"); err == nil {
		t.Fatal("accepted malformed hash")
	}
	if _, err := c.WaitForInclusion(cancelled, hashText); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled confirmation reached RPC")
	}
}
