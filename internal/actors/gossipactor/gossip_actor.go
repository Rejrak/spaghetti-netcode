package gossipactor

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"os"
	"time"

	configactors "spaghetti/internal/actors/config"
	"spaghetti/internal/configcluster"
	"spaghetti/internal/gossip"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/grpc"
)

type PeerInfo struct {
	NodeID  configcluster.NodeID
	Address string
}

type peerConnState struct {
	conn        *grpc.ClientConn
	client      gossip.ConfigSyncClient
	backoff     time.Duration
	nextAttempt time.Time
}

type Start struct{}

type GossipActor struct {
	localNodeID configcluster.NodeID
	configPID   *actor.PID
	peers       []PeerInfo

	// connection pool & backoff state per peer
	peerStates map[string]*peerConnState

	tickInterval    time.Duration
	rpcTimeout      time.Duration
	maxPeersPerTick int

	// dial backoff configuration
	baseBackoff time.Duration
	maxBackoff  time.Duration

	stopCh chan struct{}
}

func NewGossipActor(localNodeID configcluster.NodeID, configPID *actor.PID, peers []PeerInfo) actor.Producer {
	return func() actor.Receiver {
		return &GossipActor{
			localNodeID:     localNodeID,
			configPID:       configPID,
			peers:           peers,
			peerStates:      make(map[string]*peerConnState),
			tickInterval:    3 * time.Second,
			rpcTimeout:      2 * time.Second,
			maxPeersPerTick: 3,
			baseBackoff:     500 * time.Millisecond,
			maxBackoff:      30 * time.Second,
			stopCh:          make(chan struct{}),
		}
	}
}

func (a *GossipActor) Receive(c *actor.Context) {
	switch c.Message().(type) {
	case actor.Started:
		slog.Info("[gossip] started",
			"node", a.localNodeID,
			"peers", a.peers,
			"tickInterval", a.tickInterval,
			"rpcTimeout", a.rpcTimeout,
		)
		c.Send(c.PID(), Start{})

	case actor.Stopped:
		slog.Info("[gossip] stopped", "node", a.localNodeID)
		close(a.stopCh)
		// best-effort shutdown of pooled connections
		for _, st := range a.peerStates {
			if st != nil && st.conn != nil {
				_ = st.conn.Close()
			}
		}

	case Start:
		go a.loop(c.Engine())
	}
}

func (a *GossipActor) loop(engine *actor.Engine) {
	ticker := time.NewTicker(a.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.tick(engine)
		case <-a.stopCh:
			return
		}
	}
}

func (a *GossipActor) tick(engine *actor.Engine) {
	if a.configPID == nil {
		slog.Warn("[gossip] tick skipped: configPID is nil", "node", a.localNodeID)
		return
	}
	state, err := a.getState(engine)
	if err != nil {
		slog.Warn("[gossip] failed to get local state", "node", a.localNodeID, "err", err)
		return
	}

	if os.Getenv("SPAGHETTI_FORCE_CONFIG_BUMP") == "1" {
		go func() {
			time.Sleep(3 * time.Second)

			reply := make(chan configactors.State, 1)
			engine.Send(a.configPID, &configactors.GetState{Reply: reply})
			state := <-reply

			newCfg := state.Snapshot.Data
			newCfg.AttributesBaseURL = "http://bumped-from-node1:9999"

			done := make(chan error, 1)
			engine.Send(a.configPID, &configactors.LocalUpdate{
				NewConfig: newCfg,
				Reply:     done,
			})
			if err := <-done; err != nil {
				slog.Error("[config] forced bump failed", "err", err)
			} else {
				slog.Info("[config] forced bump applied")
			}
		}()
	}

	// log versione locale ad ogni tick
	slog.Debug("[gossip] tick",
		"node", a.localNodeID,
		"version", state.Snapshot.Version,
		"hash", state.Snapshot.Hash,
		"peers", len(a.peers),
	)

	peers := a.samplePeers()
	for _, peer := range peers {
		a.syncWithPeer(engine, state, peer)
	}
}

func (a *GossipActor) samplePeers() []PeerInfo {
	if len(a.peers) <= 1 || a.maxPeersPerTick <= 0 {
		return a.peers
	}
	out := make([]PeerInfo, len(a.peers))
	copy(out, a.peers)
	rand.Shuffle(len(out), func(i, j int) {
		out[i], out[j] = out[j], out[i]
	})
	if len(out) > a.maxPeersPerTick {
		out = out[:a.maxPeersPerTick]
	}
	return out
}

func (a *GossipActor) syncWithPeer(engine *actor.Engine, state configactors.State, peer PeerInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), a.rpcTimeout)
	defer cancel()

	st := a.peerStateFor(peer)
	now := time.Now()
	if !st.nextAttempt.IsZero() && now.Before(st.nextAttempt) {
		slog.Debug("[gossip] skipping peer due to backoff",
			"node", a.localNodeID,
			"peerNode", peer.NodeID,
			"peerAddr", peer.Address,
			"nextAttempt", st.nextAttempt,
		)
		return
	}

	if st.client == nil {
		conn, err := gossip.DialConfigSync(ctx, peer.Address)
		if err != nil {
			a.handlePeerError(peer, st, "dial", err)
			return
		}
		st.conn = conn
		st.client = gossip.NewConfigSyncClient(conn)
		st.backoff = 0
		st.nextAttempt = time.Time{}
	}

	client := st.client

	versionResp, err := client.GossipVersion(ctx, &gossip.ConfigVersion{
		NodeID:  string(a.localNodeID),
		Version: uint64(state.Snapshot.Version),
	})
	if err != nil {
		a.handlePeerError(peer, st, "GossipVersion", err)
		return
	}

	// reset backoff su successo
	st.backoff = 0
	st.nextAttempt = time.Time{}

	localVersion := uint64(state.Snapshot.Version)
	localHash := state.Snapshot.Hash
	localWriter := string(state.LastWriter.NodeID)

	slog.Debug("[gossip] version check",
		"node", a.localNodeID,
		"peerNode", peer.NodeID,
		"peerAddr", peer.Address,
		"localVersion", localVersion,
		"localHash", localHash,
		"localWriter", localWriter,
		"remoteVersion", versionResp.Version,
		"remoteHash", versionResp.Hash,
		"remoteWriter", versionResp.LastWriter,
	)

	switch {
	case versionResp.Version > localVersion:
		slog.Info("[gossip] pulling from peer",
			"node", a.localNodeID,
			"peerNode", peer.NodeID,
			"peerAddr", peer.Address,
			"localVersion", localVersion,
			"remoteVersion", versionResp.Version,
		)
		a.pullFromPeer(engine, client, localVersion)

	case versionResp.Version < localVersion:
		slog.Info("[gossip] pushing to peer",
			"node", a.localNodeID,
			"peerNode", peer.NodeID,
			"peerAddr", peer.Address,
			"localVersion", localVersion,
			"remoteVersion", versionResp.Version,
		)
		a.pushToPeer(client, state)

	default: // same version
		if versionResp.Hash == localHash {
			slog.Debug("[gossip] in sync with peer",
				"node", a.localNodeID,
				"peerNode", peer.NodeID,
				"peerAddr", peer.Address,
			)
			return
		}

		remoteWriter := versionResp.LastWriter

		// deterministico: versione uguale ma hash diverso
		if remoteWriter == "" || localWriter == "" {
			// fallback vecchio comportamento
			slog.Info("[gossip] pulling from peer (same version, hash mismatch, missing writer)",
				"node", a.localNodeID,
				"peerNode", peer.NodeID,
				"peerAddr", peer.Address,
				"localVersion", localVersion,
				"remoteVersion", versionResp.Version,
			)
			a.pullFromPeer(engine, client, localVersion)
			return
		}

		switch {
		case remoteWriter > localWriter:
			slog.Info("[gossip] pulling from peer (writer wins)",
				"node", a.localNodeID,
				"peerNode", peer.NodeID,
				"peerAddr", peer.Address,
				"localVersion", localVersion,
				"remoteVersion", versionResp.Version,
				"localWriter", localWriter,
				"remoteWriter", remoteWriter,
			)
			a.pullFromPeer(engine, client, localVersion)

		case remoteWriter < localWriter:
			slog.Info("[gossip] pushing to peer (we are writer winner)",
				"node", a.localNodeID,
				"peerNode", peer.NodeID,
				"peerAddr", peer.Address,
				"localVersion", localVersion,
				"remoteVersion", versionResp.Version,
				"localWriter", localWriter,
				"remoteWriter", remoteWriter,
			)
			a.pushToPeer(client, state)

		default:
			// stesso writer ma hash diverso: non facciamo niente per evitare oscillazioni
			slog.Warn("[gossip] conflict: same writer but different hash, keeping local config",
				"node", a.localNodeID,
				"peerNode", peer.NodeID,
				"peerAddr", peer.Address,
				"version", localVersion,
				"writer", localWriter,
			)
		}
	}
}

func (a *GossipActor) pullFromPeer(engine *actor.Engine, client gossip.ConfigSyncClient, localVersion uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), a.rpcTimeout)
	defer cancel()

	resp, err := client.GetConfig(ctx, &gossip.GetConfigRequest{SinceVersion: localVersion})
	if err != nil {
		slog.Debug("[gossip] GetConfig failed", "node", a.localNodeID, "err", err)
		return
	}

	// Il peer può legittimamente rispondere senza payload se non ci sono cambi.
	if len(resp.ConfigJSON) == 0 || resp.Version <= localVersion {
		slog.Debug("[gossip] no config changes from peer",
			"node", a.localNodeID,
			"fromNode", resp.NodeID,
			"localVersion", localVersion,
			"remoteVersion", resp.Version,
		)
		return
	}

	var cfg configcluster.ClusterConfig
	if err := json.Unmarshal(resp.ConfigJSON, &cfg); err != nil {
		slog.Debug("[gossip] invalid config json", "node", a.localNodeID, "err", err)
		return
	}

	slog.Info("[gossip] received snapshot",
		"node", a.localNodeID,
		"fromNode", resp.NodeID,
		"version", resp.Version,
		"hash", resp.Hash,
	)

	reply := make(chan bool, 1)
	engine.Send(a.configPID, &configactors.RemoteSnapshot{
		Snapshot: configcluster.ConfigSnapshot{
			Version: configcluster.Version(resp.Version),
			Data:    cfg,
			Hash:    resp.Hash,
		},
		FromNode: configcluster.NodeID(resp.NodeID),
		Reply:    reply,
	})
	select {
	case applied := <-reply:
		slog.Info("[gossip] remote snapshot handled",
			"node", a.localNodeID,
			"fromNode", resp.NodeID,
			"version", resp.Version,
			"applied", applied,
		)
	case <-ctx.Done():
		slog.Warn("[gossip] remote snapshot apply timeout",
			"node", a.localNodeID,
			"fromNode", resp.NodeID,
			"version", resp.Version,
		)
	}
}

func (a *GossipActor) pushToPeer(client gossip.ConfigSyncClient, state configactors.State) {
	ctx, cancel := context.WithTimeout(context.Background(), a.rpcTimeout)
	defer cancel()

	payload, err := json.Marshal(state.Snapshot.Data)
	if err != nil {
		slog.Debug("[gossip] marshal config failed", "node", a.localNodeID, "err", err)
		return
	}
	_, err = client.PushConfig(ctx, &gossip.ConfigSnapshot{
		NodeID:     string(state.LastWriter.NodeID),
		Version:    uint64(state.Snapshot.Version),
		ConfigJSON: payload,
		Hash:       state.Snapshot.Hash,
	})
	if err != nil {
		slog.Debug("[gossip] PushConfig failed",
			"node", a.localNodeID,
			"peerWriter", state.LastWriter.NodeID,
			"err", err,
		)
		return
	}

	slog.Info("[gossip] pushed snapshot to peer",
		"node", a.localNodeID,
		"writerNode", state.LastWriter.NodeID,
		"version", state.Snapshot.Version,
		"hash", state.Snapshot.Hash,
	)
}

func (a *GossipActor) getState(engine *actor.Engine) (configactors.State, error) {
	reply := make(chan configactors.State, 1)
	engine.Send(a.configPID, &configactors.GetState{Reply: reply})
	select {
	case state := <-reply:
		return state, nil
	case <-time.After(a.rpcTimeout):
		return configactors.State{}, context.DeadlineExceeded
	}
}

func (a *GossipActor) peerStateFor(peer PeerInfo) *peerConnState {
	if a.peerStates == nil {
		a.peerStates = make(map[string]*peerConnState)
	}
	st, ok := a.peerStates[peer.Address]
	if !ok {
		st = &peerConnState{}
		a.peerStates[peer.Address] = st
	}
	return st
}

func (a *GossipActor) nextBackoff(prev time.Duration) time.Duration {
	base := a.baseBackoff
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	max := a.maxBackoff
	if max <= 0 {
		max = 30 * time.Second
	}
	if prev <= 0 {
		prev = base
	} else {
		prev *= 2
		if prev > max {
			prev = max
		}
	}
	return prev
}

// applica errore+backoff con jitter
func (a *GossipActor) handlePeerError(peer PeerInfo, st *peerConnState, stage string, err error) {
	if err != nil {
		slog.Debug("[gossip] "+stage+" failed",
			"node", a.localNodeID,
			"peer", peer.Address,
			"err", err,
		)
	}
	if st == nil {
		return
	}
	if st.conn != nil {
		_ = st.conn.Close()
		st.conn = nil
		st.client = nil
	}
	prev := st.backoff
	next := a.nextBackoff(prev)

	// jitter in [next/2, next]
	jitterRange := next / 2
	var jitter time.Duration
	if jitterRange > 0 {
		jitter = time.Duration(rand.Int63n(int64(jitterRange) + 1))
	}
	backoffWithJitter := next/2 + jitter

	st.backoff = next
	st.nextAttempt = time.Now().Add(backoffWithJitter)
}
