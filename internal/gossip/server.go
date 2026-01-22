package gossip

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	configactors "spaghetti/internal/actors/config"
	"spaghetti/internal/configcluster"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/grpc"
)

type ConfigSyncService struct {
	engine    *actor.Engine
	configPID *actor.PID
	localNode configcluster.NodeID
}

func NewConfigSyncService(engine *actor.Engine, configPID *actor.PID, localNode configcluster.NodeID) *ConfigSyncService {
	return &ConfigSyncService{
		engine:    engine,
		configPID: configPID,
		localNode: localNode,
	}
}

func (s *ConfigSyncService) GossipVersion(ctx context.Context, _ *ConfigVersion) (*ConfigVersionResponse, error) {
	state, err := s.getState(ctx)
	if err != nil {
		return nil, err
	}
	return &ConfigVersionResponse{
		Version:    uint64(state.Snapshot.Version),
		Hash:       state.Snapshot.Hash,
		LastWriter: string(state.LastWriter.NodeID),
	}, nil
}

func (s *ConfigSyncService) GetConfig(ctx context.Context, req *GetConfigRequest) (*ConfigSnapshot, error) {
	state, err := s.getState(ctx)
	if err != nil {
		return nil, err
	}

	// Se il caller è già allineato (o avanti), non inviamo il full snapshot.
	currentVersion := uint64(state.Snapshot.Version)
	if req != nil && req.SinceVersion >= currentVersion {
		return &ConfigSnapshot{
			NodeID:  string(state.LastWriter.NodeID),
			Version: currentVersion,
			Hash:    state.Snapshot.Hash,
		}, nil
	}

	payload, err := json.Marshal(state.Snapshot.Data)
	if err != nil {
		return nil, err
	}
	return &ConfigSnapshot{
		NodeID:     string(state.LastWriter.NodeID),
		Version:    currentVersion,
		ConfigJSON: payload,
		Hash:       state.Snapshot.Hash,
	}, nil
}

func (s *ConfigSyncService) PushConfig(ctx context.Context, in *ConfigSnapshot) (*Ack, error) {
	if in == nil {
		return nil, errors.New("nil snapshot")
	}
	var cfg configcluster.ClusterConfig
	if err := json.Unmarshal(in.ConfigJSON, &cfg); err != nil {
		return nil, err
	}
	snapshot := configcluster.ConfigSnapshot{
		Version: configcluster.Version(in.Version),
		Data:    cfg,
		Hash:    in.Hash,
	}
	reply := make(chan bool, 1)
	s.engine.Send(s.configPID, &configactors.RemoteSnapshot{
		Snapshot: snapshot,
		FromNode: configcluster.NodeID(in.NodeID),
		Reply:    reply,
	})
	select {
	case applied := <-reply:
		return &Ack{Applied: applied}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *ConfigSyncService) getState(ctx context.Context) (configactors.State, error) {
	if s.engine == nil || s.configPID == nil {
		return configactors.State{}, errors.New("config actor not available")
	}
	reply := make(chan configactors.State, 1)
	s.engine.Send(s.configPID, &configactors.GetState{Reply: reply})
	select {
	case state := <-reply:
		return state, nil
	case <-ctx.Done():
		return configactors.State{}, ctx.Err()
	}
}

func ServeConfigSync(addr string, engine *actor.Engine, configPID *actor.PID, localNode configcluster.NodeID) (*grpc.Server, net.Listener, error) {
	RegisterJSONCodec()
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	server := grpc.NewServer(grpc.ForceServerCodec(jsonCodec{}))
	RegisterConfigSyncServer(server, NewConfigSyncService(engine, configPID, localNode))
	go server.Serve(lis)
	return server, lis, nil
}
