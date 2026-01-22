package gossip

import (
	"context"

	"google.golang.org/grpc"
)

const (
	ConfigSync_GossipVersion_FullMethodName = "/gossip.ConfigSync/GossipVersion"
	ConfigSync_GetConfig_FullMethodName     = "/gossip.ConfigSync/GetConfig"
	ConfigSync_PushConfig_FullMethodName    = "/gossip.ConfigSync/PushConfig"
)

type ConfigSyncServer interface {
	GossipVersion(context.Context, *ConfigVersion) (*ConfigVersionResponse, error)
	GetConfig(context.Context, *GetConfigRequest) (*ConfigSnapshot, error)
	PushConfig(context.Context, *ConfigSnapshot) (*Ack, error)
}

func RegisterConfigSyncServer(s grpc.ServiceRegistrar, srv ConfigSyncServer) {
	s.RegisterService(&ConfigSync_ServiceDesc, srv)
}

func _ConfigSync_GossipVersion_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(ConfigVersion)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ConfigSyncServer).GossipVersion(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: ConfigSync_GossipVersion_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ConfigSyncServer).GossipVersion(ctx, req.(*ConfigVersion))
	}
	return interceptor(ctx, in, info, handler)
}

func _ConfigSync_GetConfig_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(GetConfigRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ConfigSyncServer).GetConfig(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: ConfigSync_GetConfig_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ConfigSyncServer).GetConfig(ctx, req.(*GetConfigRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ConfigSync_PushConfig_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(ConfigSnapshot)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ConfigSyncServer).PushConfig(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: ConfigSync_PushConfig_FullMethodName,
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ConfigSyncServer).PushConfig(ctx, req.(*ConfigSnapshot))
	}
	return interceptor(ctx, in, info, handler)
}

// ConfigSync_ServiceDesc is the grpc.ServiceDesc for ConfigSync service.
var ConfigSync_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "gossip.ConfigSync",
	HandlerType: (*ConfigSyncServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GossipVersion",
			Handler:    _ConfigSync_GossipVersion_Handler,
		},
		{
			MethodName: "GetConfig",
			Handler:    _ConfigSync_GetConfig_Handler,
		},
		{
			MethodName: "PushConfig",
			Handler:    _ConfigSync_PushConfig_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "internal/gossip/gossip.proto",
}

type ConfigSyncClient interface {
	GossipVersion(ctx context.Context, in *ConfigVersion, opts ...grpc.CallOption) (*ConfigVersionResponse, error)
	GetConfig(ctx context.Context, in *GetConfigRequest, opts ...grpc.CallOption) (*ConfigSnapshot, error)
	PushConfig(ctx context.Context, in *ConfigSnapshot, opts ...grpc.CallOption) (*Ack, error)
}

type configSyncClient struct {
	cc *grpc.ClientConn
}

func NewConfigSyncClient(cc *grpc.ClientConn) ConfigSyncClient {
	return &configSyncClient{cc: cc}
}

func (c *configSyncClient) GossipVersion(ctx context.Context, in *ConfigVersion, opts ...grpc.CallOption) (*ConfigVersionResponse, error) {
	out := new(ConfigVersionResponse)
	err := c.cc.Invoke(ctx, ConfigSync_GossipVersion_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *configSyncClient) GetConfig(ctx context.Context, in *GetConfigRequest, opts ...grpc.CallOption) (*ConfigSnapshot, error) {
	out := new(ConfigSnapshot)
	err := c.cc.Invoke(ctx, ConfigSync_GetConfig_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *configSyncClient) PushConfig(ctx context.Context, in *ConfigSnapshot, opts ...grpc.CallOption) (*Ack, error) {
	out := new(Ack)
	err := c.cc.Invoke(ctx, ConfigSync_PushConfig_FullMethodName, in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}
