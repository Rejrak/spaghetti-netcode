package packets

import (
	"log/slog"

	"google.golang.org/protobuf/proto"
)

func CosmosPacketToBytes(packet *CosmosPacket) ([]byte, error) {
	out, err := proto.Marshal(packet)
	if err != nil {
		slog.Info("error marshalling packet: %v", err)
		return nil, err
	}
	msgLen := len(out)
	lengthPrefix := []byte{
		byte(msgLen >> 24),
		byte(msgLen >> 16),
		byte(msgLen >> 8),
		byte(msgLen),
	}
	fullPacket := append(lengthPrefix, out...)
	return fullPacket, nil
}
