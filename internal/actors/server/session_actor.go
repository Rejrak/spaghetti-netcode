package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"spaghetti/internal/pkg/packets"
	"spaghetti/internal/storage/sqlite"
	"spaghetti/internal/user"
	"time"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/protobuf/proto"
)

// Parents -> Server -> Session
type handler struct{}

func newHandler() actor.Receiver {
	return &handler{}
}

func (handler) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		fmt.Printf("\nHandler started with PID: %v", c.PID())
	case actor.Stopped:
		for i := 0; i < 1; i++ {
			fmt.Printf("\r handler %v stopping in %d", c.PID(), 1-i)
			time.Sleep(time.Second)
		}
		fmt.Println("\nhandler stopped")
	case []byte:
		packet := &packets.CosmosPacket{}
		err := proto.Unmarshal(msg, packet)
		if err != nil {
			slog.Info("\nerror unmarshalling data: %v", slog.Attr{Key: "Error", Value: slog.AnyValue(err)})
		}

		switch m := packet.Msg.(type) {
		case *packets.CosmosPacket_AuthMessage:
			fmt.Println("\nHandler: Received auth message:", m)
			c.Send(c.Parent(), m.AuthMessage)
		default:
			fmt.Println("\nTipo di messaggio non riconosciuto")
		}

	}
}

// Parents -> Server
type session struct {
	conn net.Conn
	repo *sqlite.Repo
}

func newSession(conn net.Conn) actor.Producer {
	return func() actor.Receiver {
		return &session{
			conn: conn,
		}
	}
}

func (s *session) readUserAttributes(c context.Context, address string) (*user.Attributes, error) {
	repo, err := sqlite.Open("./authblock.db")
	if err != nil {
		slog.Info("sqlite open error: %v", err)
		return nil, err
	}
	s.repo = repo
	userAttrs, updated, ok, err := s.repo.GetAttrs(c, address)
	if err != nil {
		slog.Error("Failed to get user attributes", "err", err)
		return nil, err
	}
	slog.Info("Address Attrs", "attrs", userAttrs, "updated", updated, "ok", ok, "err", err)
	if !ok {
		s.repo.EnsureAddress(c, address, "")
		return nil, fmt.Errorf("attributes not found")
	}
	return userAttrs, nil
}

func (s *session) Receive(c *actor.Context) {

	switch msg := c.Message().(type) {
	case actor.Started:
		c.SpawnChild(newHandler, "handler", actor.WithID("session"))
		slog.Info("new connection", "addr", s.conn.RemoteAddr())
		go s.readLoop(c)
	case actor.Stopped:
		s.conn.Close()
	case *packets.CosmosPacket:
		slog.Info("Handler: Received Cosmos packet:", msg)
	case *packets.AuthMessage:
		userAttrs, _ := s.readUserAttributes(c.Context(), msg.Address)
		resp := &packets.CosmosPacket{
			SenderId: msg.Address,
			Msg: &packets.CosmosPacket_ResponseMessage{
				ResponseMessage: &packets.ResponseMessage{
					Success: userAttrs.CanCreate,
					Message: "",
				},
			},
		}
		data, err := packets.CosmosPacketToBytes(resp)
		if err != nil {
			slog.Error("failed to serialize response", "err", err)
			return
		}
		if _, err := s.conn.Write(data); err != nil {
			slog.Error("write failed", "err", err)
		}

		slog.Info("Response Sended", "err", err)
	}
}

func (s *session) readLoop(c *actor.Context) {
	buf := make([]byte, 1024)
	var dataBuffer []byte
	var handlerPID = fmt.Sprintf("%s/handler/session", c.PID().ID)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			slog.Error("conn read error", "err", err)
			break
		}
		dataBuffer = append(dataBuffer, buf[:n]...)
		for {
			if len(dataBuffer) < 4 {
				break
			}
			msgLen := int(dataBuffer[0])<<24 | int(dataBuffer[1])<<16 | int(dataBuffer[2])<<8 | int(dataBuffer[3])
			if len(dataBuffer) < 4+msgLen {
				break
			}
			packetBytes := dataBuffer[4 : 4+msgLen]
			c.Send(c.Child(handlerPID), packetBytes)
			dataBuffer = dataBuffer[4+msgLen:]
		}
	}
	c.Send(c.Parent(), &connRem{pid: c.PID()})
	c.Engine().Poison(c.PID())
}
