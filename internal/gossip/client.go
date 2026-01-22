package gossip

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func DialConfigSync(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	RegisterJSONCodec()
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
	)
	if err != nil {
		return nil, err
	}

	return conn, nil
}
