package gossip_test

import (
	"testing"
	"time"

	configactors "spaghetti/internal/actors/config"
	"spaghetti/internal/actors/gossipactor"
	"spaghetti/internal/configcluster"
	"spaghetti/internal/gossip"

	"github.com/anthdm/hollywood/actor"
)

type testNode struct {
	engine    *actor.Engine
	configPID *actor.PID
	addr      string
	stop      func()
}

func startNode(t *testing.T, nodeID string, initial configcluster.ClusterConfig) testNode {
	t.Helper()

	engine, err := actor.NewEngine(actor.NewEngineConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	initialSnap := configcluster.ConfigSnapshot{Version: 1, Data: initial}
	configPID := engine.Spawn(configactors.NewConfigActor(initialSnap, configcluster.NodeID(nodeID)), "config")

	server, listener, err := gossip.ServeConfigSync("127.0.0.1:0", engine, configPID, configcluster.NodeID(nodeID))
	if err != nil {
		t.Fatalf("ServeConfigSync: %v", err)
	}

	stop := func() {
		server.GracefulStop()
		_ = listener.Close()
		engine.Poison(configPID)
	}

	return testNode{
		engine:    engine,
		configPID: configPID,
		addr:      listener.Addr().String(),
		stop:      stop,
	}
}

func getSnapshot(t *testing.T, engine *actor.Engine, pid *actor.PID) configcluster.ConfigSnapshot {
	t.Helper()
	reply := make(chan configcluster.ConfigSnapshot, 1)
	engine.Send(pid, &configactors.GetConfig{Reply: reply})
	select {
	case snap := <-reply:
		return snap
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for GetConfig")
	}
	return configcluster.ConfigSnapshot{}
}

func TestGossipIntegrationBasic(t *testing.T) {
	nodeA := startNode(t, "node-a", configcluster.ClusterConfig{DBPath: "db-a"})
	defer nodeA.stop()
	nodeB := startNode(t, "node-b", configcluster.ClusterConfig{DBPath: "db-b"})
	defer nodeB.stop()

	nodeA.engine.Spawn(gossipactor.NewGossipActor(
		"node-a",
		nodeA.configPID,
		[]gossipactor.PeerInfo{{NodeID: "node-b", Address: nodeB.addr}},
	), "gossip")
	nodeB.engine.Spawn(gossipactor.NewGossipActor(
		"node-b",
		nodeB.configPID,
		[]gossipactor.PeerInfo{{NodeID: "node-a", Address: nodeA.addr}},
	), "gossip")

	updateReply := make(chan error, 1)
	nodeA.engine.Send(nodeA.configPID, &configactors.LocalUpdate{
		NewConfig: configcluster.ClusterConfig{DBPath: "db-updated"},
		Reply:     updateReply,
	})
	select {
	case err := <-updateReply:
		if err != nil {
			t.Fatalf("LocalUpdate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting LocalUpdate reply")
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snap := getSnapshot(t, nodeB.engine, nodeB.configPID)
		if snap.Version >= 2 && snap.Data.DBPath == "db-updated" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("node B did not converge to updated config")
}
