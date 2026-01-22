package config

import (
	"testing"
	"time"

	"spaghetti/internal/configcluster"

	"github.com/anthdm/hollywood/actor"
)

type testParent struct {
	initial     configcluster.ConfigSnapshot
	nodeID      configcluster.NodeID
	configPIDCh chan *actor.PID
	updatesCh   chan configcluster.ConfigSnapshot
	configActor *actor.PID
}

func (p *testParent) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		props := NewConfigActor(p.initial, p.nodeID)
		p.configActor = c.SpawnChild(props, "config")
		if p.configPIDCh != nil {
			p.configPIDCh <- p.configActor
		}
	case *ConfigUpdated:
		if p.updatesCh != nil {
			p.updatesCh <- msg.Snapshot
		}
	}
}

func TestConfigActorUpdates(t *testing.T) {
	engine, err := actor.NewEngine(actor.NewEngineConfig())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	initial := configcluster.ConfigSnapshot{
		Version: 1,
		Data:    configcluster.ClusterConfig{DBPath: "db1"},
	}

	configPIDCh := make(chan *actor.PID, 1)
	updatesCh := make(chan configcluster.ConfigSnapshot, 4)

	parent := &testParent{
		initial:     initial,
		nodeID:      "node-a",
		configPIDCh: configPIDCh,
		updatesCh:   updatesCh,
	}
	parentPID := engine.Spawn(actor.Producer(func() actor.Receiver { return parent }), "parent")
	t.Cleanup(func() {
		engine.Poison(parentPID)
	})

	var configPID *actor.PID
	select {
	case configPID = <-configPIDCh:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for config actor pid")
	}

	replyErr := make(chan error, 1)
	newCfg := configcluster.ClusterConfig{DBPath: "db2"}
	engine.Send(configPID, &LocalUpdate{NewConfig: newCfg, Reply: replyErr})
	select {
	case err := <-replyErr:
		if err != nil {
			t.Fatalf("LocalUpdate error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting LocalUpdate reply")
	}

	select {
	case snap := <-updatesCh:
		if snap.Version != 2 {
			t.Fatalf("expected version 2, got %d", snap.Version)
		}
		if snap.Data.DBPath != "db2" {
			t.Fatalf("expected DBPath db2, got %s", snap.Data.DBPath)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ConfigUpdated")
	}

	replyOld := make(chan bool, 1)
	oldSnap := configcluster.ConfigSnapshot{
		Version: 1,
		Data:    configcluster.ClusterConfig{DBPath: "old"},
	}
	engine.Send(configPID, &RemoteSnapshot{Snapshot: oldSnap, FromNode: "node-z", Reply: replyOld})
	select {
	case applied := <-replyOld:
		if applied {
			t.Fatal("expected old snapshot to be ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for old snapshot reply")
	}

	replyNew := make(chan bool, 1)
	newSnap := configcluster.ConfigSnapshot{
		Version: 3,
		Data:    configcluster.ClusterConfig{DBPath: "db3"},
	}
	engine.Send(configPID, &RemoteSnapshot{Snapshot: newSnap, FromNode: "node-z", Reply: replyNew})
	select {
	case applied := <-replyNew:
		if !applied {
			t.Fatal("expected new snapshot to be applied")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for new snapshot reply")
	}

	getReply := make(chan configcluster.ConfigSnapshot, 1)
	engine.Send(configPID, &GetConfig{Reply: getReply})
	select {
	case snap := <-getReply:
		if snap.Version != 3 {
			t.Fatalf("expected version 3, got %d", snap.Version)
		}
		if snap.Data.DBPath != "db3" {
			t.Fatalf("expected DBPath db3, got %s", snap.Data.DBPath)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for GetConfig reply")
	}
}
