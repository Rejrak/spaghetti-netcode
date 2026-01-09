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
	"sync"
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

var framePool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
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

	closeOnce sync.Once
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

		engine := c.Engine()
		selfPID := c.PID()
		handlerPID := s.handlerPID

		go s.readLoop(engine, selfPID, handlerPID)

	case *stopSession:
		if msg.err != nil {
			slog.Info("[session]-> stopping due to error", "err", msg.err)
		}

		s.closeConn()
		if s.cancel != nil {
			s.cancel()
		}

		c.Send(c.Parent(), &connRem{pid: c.PID()})
		c.Engine().Poison(c.PID())

	case actor.Stopped:
		if s.cancel != nil {
			s.cancel()
		}
		s.closeConn()

	case *packets.CosmosPacket:
		slog.Info("[session]-> Handler: Received Cosmos packet", "packet", msg)

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
		slog.Info("[session]-> Handler: Received AuthMessage", "message", msg)

	default:
		slog.Warn("[session]-> unknown message", "msg", msg)
	}
}

func (s *session) closeConn() {
	s.closeOnce.Do(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

func (s *session) readUserAttributes(c context.Context, address string) (*user.Attributes, error) {
	userAttrs, updated, ok, err := s.repo.GetAttrsExtended(c, address)
	if err != nil {
		slog.Error("[session]-> Failed to get user attributes", "err", err)
		return nil, err
	}
	slog.Info("[session]-> Address Attrs", "attrs", userAttrs, "updated", updated, "ok", ok, "err", err)
	if !ok {
		_ = s.repo.EnsureAddress(c, address, "")
		return nil, fmt.Errorf("attributes not found")
	}
	return userAttrs, nil
}

// var count int64 = 0

func (s *session) checkOperationAndPermissions(op string, attrs *user.Attributes, msg *packets.AuthMessage) *packets.CosmosPacket_ResponseMessage {
	// atomic.AddInt64(&count, 1)
	// success := count%10 != 0

	return &packets.CosmosPacket_ResponseMessage{
		ResponseMessage: &packets.ResponseMessage{
			Success: true,
			Message: "",
		},
	}
}

func (s *session) readLoop(engine *actor.Engine, selfPID, handlerPID *actor.PID) {
	buf := make([]byte, 4096)
	dataBuffer := make([]byte, 0, 8192)
	lastActivity := time.Now()

	for {
		select {
		case <-s.ctx.Done():
			engine.Send(selfPID, &stopSession{err: context.Canceled})
			return
		default:
		}

		_ = s.conn.SetReadDeadline(time.Now().Add(readDeadline))

		n, err := s.conn.Read(buf)
		if err != nil {
			// Se la conn è stata chiusa da noi, spesso err != Timeout; va bene: stop.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if time.Since(lastActivity) >= idleTimeout {
					engine.Send(selfPID, &stopSession{err: fmt.Errorf("idle timeout after %s", idleTimeout)})
					return
				}
				continue
			}

			engine.Send(selfPID, &stopSession{err: err})
			return
		}
		lastActivity = time.Now()

		dataBuffer = append(dataBuffer, buf[:n]...)
		if len(dataBuffer) > maxFrameSize+4 {
			engine.Send(selfPID, &stopSession{err: fmt.Errorf("buffer exceeds limit: %d", len(dataBuffer))})
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
				engine.Send(selfPID, &stopSession{err: fmt.Errorf("invalid frame size: %d", msgLen)})
				return
			}

			if len(dataBuffer) < 4+msgLen {
				break
			}

			packetBytes := dataBuffer[4 : 4+msgLen]

			pb := framePool.Get().(*[]byte)
			b := *pb
			if cap(b) < len(packetBytes) {
				b = make([]byte, len(packetBytes))
			} else {
				b = b[:len(packetBytes)]
			}
			copy(b, packetBytes)

			engine.Send(handlerPID, b)

			dataBuffer = dataBuffer[4+msgLen:]

			if len(dataBuffer) == 0 {
				dataBuffer = make([]byte, 0, 8192)
				break
			}
		}
	}
}

// Parents -> Server -> Session
type handler struct{}

func newHandler() actor.Receiver { return &handler{} }

func (handler) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		slog.Info("[handler]-> started", "pid", c.PID())
	case actor.Stopped:
		slog.Info("[handler]-> stopped", "pid", c.PID())

	case []byte:
		defer func() {
			b := msg[:0]
			framePool.Put(&b)
		}()

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
