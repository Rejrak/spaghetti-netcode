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
		slog.Info("[handler]-> started with PID: %v", c.PID())
	case actor.Stopped:
		for i := 0; i < 1; i++ {
			slog.Info("\r[handler]-> stopping in %d", 1-i)
			time.Sleep(time.Second)
		}
		slog.Info("[handler]-> stopped")
	case []byte:
		packet := &packets.CosmosPacket{}
		err := proto.Unmarshal(msg, packet)
		if err != nil {
			slog.Info("[handler]-> error unmarshalling data: %v", slog.Attr{Key: "Error", Value: slog.AnyValue(err)})
		}

		switch m := packet.Msg.(type) {
		case *packets.CosmosPacket_AuthMessage:
			slog.Info("[handler]-> received auth message:", m)
			c.Send(c.Parent(), m.AuthMessage)
		default:
			slog.Info("[handler]-> unrecognized message type in CosmosPacket:", slog.Any("type", fmt.Sprintf("%T", m)))
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
		slog.Info("[session]-> sqlite open error: %v", err)
		return nil, err
	}
	s.repo = repo
	userAttrs, updated, ok, err := s.repo.GetAttrsExtended(c, address)
	if err != nil {
		slog.Error("[session]-> Failed to get user attributes", "err", err)
		return nil, err
	}
	slog.Info("[session]-> Address Attrs", "attrs", userAttrs, "updated", updated, "ok", ok, "err", err)
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
		slog.Info("[session]-> new connection", "addr", s.conn.RemoteAddr())
		go s.readLoop(c)
	case actor.Stopped:
		s.conn.Close()
	case *packets.CosmosPacket:
		slog.Info("[session]-> Handler: Received Cosmos packet:", msg)
	case *packets.AuthMessage:
		userAttrs, _ := s.readUserAttributes(c.Context(), msg.Address)
		response := s.checkOperationAndPermissions(msg.Operation, userAttrs)
		resp := &packets.CosmosPacket{
			SenderId: msg.Address,
			Msg:      response,
		}
		data, err := packets.CosmosPacketToBytes(resp)
		if err != nil {
			slog.Error("[session]-> failed to serialize response", "err", err)
			return
		}
		if _, err := s.conn.Write(data); err != nil {
			slog.Error("[session]-> write failed", "err", err)
		}
		slog.Info("[session]-> Response Sended", "err", err)

	}
}

func (s *session) checkOperationAndPermissions(op string, attrs *user.Attributes) *packets.CosmosPacket_ResponseMessage {
	switch op {
	case "/cosmos.bank.v1beta1.MsgSend":
		return &packets.CosmosPacket_ResponseMessage{
			ResponseMessage: &packets.ResponseMessage{
				Success: attrs.Perms["supply.harvest.create"],
				Message: "Permission to send tokens",
			}}
	default:
		return &packets.CosmosPacket_ResponseMessage{
			ResponseMessage: &packets.ResponseMessage{
				Success: true,
				Message: "Unknown operation, allowing by default",
			}}
	}
}

func (s *session) readLoop(c *actor.Context) {
	buf := make([]byte, 1024)
	var dataBuffer []byte
	var handlerPID = fmt.Sprintf("%s/handler/session", c.PID().ID)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			slog.Error("[session]-> conn read error", "err", err)
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
