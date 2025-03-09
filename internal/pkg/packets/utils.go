package packets

import (
	"log/slog"

	"google.golang.org/protobuf/proto"
)

type Msg = isPacket_Msg

func NewChat(msg string) Msg {
	return &Packet_Chat{
		Chat: &ChatMessage{
			Msg: msg,
		},
	}
}

func NewPosition(x, y, z float32) Msg {
	return &Packet_Position{
		Position: &Position{
			X: x,
			Y: y,
			Z: z,
		},
	}
}

func ToBytes(packet *Packet) ([]byte, error) {
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
