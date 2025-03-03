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

func NewId(id uint64 ) Msg {
	return &Packet_Id{
		Id: &IdMessage{
			Id: id,
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
