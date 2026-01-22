package config

import (
	"log/slog"

	"spaghetti/internal/configcluster"

	"github.com/anthdm/hollywood/actor"
)

type GetConfig struct {
	Reply chan configcluster.ConfigSnapshot
}

type GetState struct {
	Reply chan State
}

type State struct {
	Snapshot   configcluster.ConfigSnapshot
	LastWriter configcluster.VersionStamp
}

type LocalUpdate struct {
	NewConfig configcluster.ClusterConfig
	Reply     chan error
}

type RemoteSnapshot struct {
	Snapshot configcluster.ConfigSnapshot
	FromNode configcluster.NodeID
	Reply    chan bool // true se applicato, false se ignorato
}

// Event interno per altri attori locali (server TCP, ecc.)
type ConfigUpdated struct {
	Snapshot configcluster.ConfigSnapshot
}

type ConfigActor struct {
	current    configcluster.ConfigSnapshot
	lastWriter configcluster.VersionStamp
	localNode  configcluster.NodeID
}

func NewConfigActor(initial configcluster.ConfigSnapshot, localNodeID configcluster.NodeID) actor.Producer {
	return func() actor.Receiver {
		snapshot := initial
		if snapshot.Hash == "" {
			snapshot.Hash = snapshot.ComputeHash()
		}

		slog.Info("[config] init",
			"node", localNodeID,
			"version", snapshot.Version,
			"hash", snapshot.Hash,
			"attrBase", snapshot.Data.AttributesBaseURL, // ad esempio
		)

		return &ConfigActor{
			current:    snapshot,
			lastWriter: configcluster.VersionStamp{Version: snapshot.Version, NodeID: localNodeID},
			localNode:  localNodeID,
		}
	}
}

func (a *ConfigActor) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {

	case *GetConfig:
		if msg.Reply != nil {
			msg.Reply <- a.current
		}

	case *GetState:
		if msg.Reply != nil {
			msg.Reply <- State{Snapshot: a.current, LastWriter: a.lastWriter}
		}

	case *LocalUpdate:
		newVersion := a.current.Version + 1
		snapshot := configcluster.ConfigSnapshot{
			Version: newVersion,
			Data:    msg.NewConfig,
		}
		snapshot.Hash = snapshot.ComputeHash()
		a.current = snapshot
		a.lastWriter = configcluster.VersionStamp{Version: newVersion, NodeID: a.localNode}

		slog.Info("[config] LOCAL update",
			"node", a.localNode,
			"version", a.current.Version,
			"hash", a.current.Hash,
		)

		c.Send(c.Parent(), &ConfigUpdated{Snapshot: a.current})
		if msg.Reply != nil {
			msg.Reply <- nil
		}

	case *RemoteSnapshot:
		incoming := configcluster.VersionStamp{Version: msg.Snapshot.Version, NodeID: msg.FromNode}

		if !incoming.IsNewerThan(a.lastWriter) {
			slog.Info("[config] REMOTE ignored",
				"node", a.localNode,
				"fromNode", msg.FromNode,
				"incomingVersion", msg.Snapshot.Version,
				"currentVersion", a.current.Version,
				"currentWriter", a.lastWriter.NodeID,
			)
			if msg.Reply != nil {
				msg.Reply <- false
			}
			return
		}

		snapshot := msg.Snapshot
		if snapshot.Hash == "" {
			snapshot.Hash = snapshot.ComputeHash()
		}
		a.current = snapshot
		a.lastWriter = incoming

		slog.Info("[config] REMOTE applied",
			"node", a.localNode,
			"fromNode", msg.FromNode,
			"version", a.current.Version,
			"hash", a.current.Hash,
		)

		c.Send(c.Parent(), &ConfigUpdated{Snapshot: a.current})
		if msg.Reply != nil {
			msg.Reply <- true
		}
	}
}
