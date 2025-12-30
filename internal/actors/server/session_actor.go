package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"spaghetti/internal/pkg/packets"
	"spaghetti/internal/remote/policy"
	"spaghetti/internal/storage/sqlite"
	"spaghetti/internal/user"
	"time"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/protobuf/proto"
)

const (
	maxFrameSize  = 1 << 20
	readDeadline  = 2 * time.Second
	writeDeadline = 5 * time.Second
	idleTimeout   = 30 * time.Second
)

type stopSession struct {
	err error
}

// Parents -> Server
type session struct {
	conn       net.Conn
	repo       *sqlite.Repo
	dynEval    *policy.DynamicEvaluator
	handlerPID *actor.PID

	// lifecycle
	ctx    context.Context
	cancel context.CancelFunc
}

func newSession(conn net.Conn, dyn *policy.DynamicEvaluator, repo *sqlite.Repo) actor.Producer {
	return func() actor.Receiver {
		return &session{
			conn:    conn,
			dynEval: dyn,
			repo:    repo,
		}
	}
}

func (s *session) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {

	case actor.Started:
		s.ctx, s.cancel = context.WithCancel(context.Background())

		s.handlerPID = c.SpawnChild(newHandler, "handler")

		slog.Info("[session]-> new connection", "addr", s.conn.RemoteAddr())

		go s.readLoop(c)

	case *stopSession:
		if msg.err != nil {
			slog.Info("[session]-> stopping due to read error", "err", msg.err)
		}
		c.Send(c.Parent(), &connRem{pid: c.PID()})
		c.Engine().Poison(c.PID())

	case actor.Stopped:
		if s.cancel != nil {
			s.cancel()
		}
		if s.conn != nil {
			_ = s.conn.Close()
		}

	case *packets.CosmosPacket:
		slog.Info("[session]-> Handler: Received Cosmos packet:", "packet", msg)

		reqID := msg.GetRequestId()
		auth := msg.GetAuthMessage()
		if auth == nil {
			slog.Error("[session]-> received CosmosPacket without AuthMessage")
			return
		}
		if reqID == "" {
			slog.Error("[session]-> missing request ID in the received packet")
			return
		}

		userAttrs, _ := s.readUserAttributes(c.Context(), auth.Address)
		response := s.checkOperationAndPermissions(auth.Operation, userAttrs, auth)

		resp := &packets.CosmosPacket{
			RequestId: reqID,
			Msg:       response,
		}
		data, err := packets.CosmosPacketToBytes(resp)
		if err != nil {
			slog.Error("[session]-> failed to serialize response", "err", err)
			return
		}

		_ = s.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := s.conn.Write(data); err != nil {
			slog.Error("[session]-> write failed", "err", err)
			c.Send(c.PID(), &stopSession{err: err})
			return
		}

	case *packets.AuthMessage:
		slog.Info("[session]-> Handler: Received AuthMessage:", "message", msg)

	default:
		slog.Warn("[session]-> unknown message", "msg", msg)
	}
}

func (s *session) readUserAttributes(c context.Context, address string) (*user.Attributes, error) {
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

func (s *session) checkOperationAndPermissions(op string, attrs *user.Attributes, msg *packets.AuthMessage) *packets.CosmosPacket_ResponseMessage {
	return &packets.CosmosPacket_ResponseMessage{
		ResponseMessage: &packets.ResponseMessage{
			Success: true,
			Message: "",
		},
	}
}

func (s *session) readLoop(c *actor.Context) {
	defer func() {
	}()

	buf := make([]byte, 4096)
	dataBuffer := make([]byte, 0, 8192)
	lastActivity := time.Now()

	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(readDeadline))

		n, err := s.conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if time.Since(lastActivity) >= idleTimeout {
					c.Send(c.PID(), &stopSession{err: fmt.Errorf("idle timeout after %s", idleTimeout)})
					return
				}
				select {
				case <-s.ctx.Done():
					c.Send(c.PID(), &stopSession{err: context.Canceled})
					return
				default:
					continue
				}
			}

			c.Send(c.PID(), &stopSession{err: err})
			return
		}
		lastActivity = time.Now()

		dataBuffer = append(dataBuffer, buf[:n]...)
		if len(dataBuffer) > maxFrameSize+4 {
			c.Send(c.PID(), &stopSession{err: fmt.Errorf("buffer exceeds limit: %d", len(dataBuffer))})
			return
		}

		for {
			if len(dataBuffer) < 4 {
				break
			}

			msgLen := int(dataBuffer[0])<<24 |
				int(dataBuffer[1])<<16 |
				int(dataBuffer[2])<<8 |
				int(dataBuffer[3])

			if msgLen <= 0 || msgLen > maxFrameSize {
				c.Send(c.PID(), &stopSession{err: fmt.Errorf("invalid frame size: %d", msgLen)})
				return
			}

			if len(dataBuffer) < 4+msgLen {
				break
			}

			packetBytes := dataBuffer[4 : 4+msgLen]

			tmp := make([]byte, len(packetBytes))
			copy(tmp, packetBytes)

			c.Send(s.handlerPID, tmp)

			dataBuffer = dataBuffer[4+msgLen:]

			if len(dataBuffer) == 0 {
				dataBuffer = make([]byte, 0, 8192)
				break
			}
		}

		select {
		case <-s.ctx.Done():
			c.Send(c.PID(), &stopSession{err: context.Canceled})
			return
		default:
		}
	}
}

// Parents -> Server -> Session
type handler struct{}

func newHandler() actor.Receiver {
	return &handler{}
}

func (handler) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		slog.Info("[handler]-> started", "pid", c.PID())
	case actor.Stopped:
		slog.Info("[handler]-> stopped", "pid", c.PID())
	case []byte:
		packet := &packets.CosmosPacket{}
		if err := proto.Unmarshal(msg, packet); err != nil {
			slog.Info("[handler]-> error unmarshalling data", "err", err)
			return
		}

		switch m := packet.Msg.(type) {
		case *packets.CosmosPacket_AuthMessage:
			slog.Info("[handler]-> received auth message", "message", m)
			c.Send(c.Parent(), packet)
		default:
			slog.Info("[handler]-> unrecognized message type", "type", fmt.Sprintf("%T", m))
		}
	}
}
