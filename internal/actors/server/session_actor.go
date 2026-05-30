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

// Parents -> Server
type session struct {
	conn    net.Conn
	repo    *sqlite.Repo
	dynEval *policy.DynamicEvaluator
	counter uint64
}

func newSession(conn net.Conn, dyn *policy.DynamicEvaluator) actor.Producer {
	return func() actor.Receiver {
		return &session{
			conn:    conn,
			dynEval: dyn,
		}
	}
}

func (s *session) readUserAttributes(c context.Context, address string) (*user.Attributes, error) {
	repo, err := sqlite.Open("./authblock.db")
	if err != nil {
		slog.Info("[session]-> sqlite open error: %v", "err", err)
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
		if _, err := s.conn.Write(data); err != nil {
			slog.Error("[session]-> write failed", "err", err)
		}
		slog.Info("[session]-> Response Sended", "err", err)

	case *packets.AuthMessage:
		slog.Info("[session]-> Handler: Received AuthMessage:", "message", msg)
	default:
		slog.Warn("[session]-> unknown message", "msg", msg)
	}
}

func (s *session) checkOperationAndPermissions(op string, attrs *user.Attributes, msg *packets.AuthMessage) *packets.CosmosPacket_ResponseMessage {
	// Comportamento CheckTx: filtro mempool.
	// Se l'utente non ha gli attributi necessari, rifiutiamo subito in CheckTx per salvare gas.
	// Se il servizio ha problemi (doubtful/degraded), rifiutiamo per prudenza (fail closed).
	// La catena in DeliverTx userà lo stato on-chain finale (tramite i batch authz).

	if attrs == nil {
		slog.Warn("[session]-> Mempool filter: no attributes found, rejecting tx", "op", op, "address", msg.Address)
		return &packets.CosmosPacket_ResponseMessage{
			ResponseMessage: &packets.ResponseMessage{
				Success: false,
				Message: "CheckTx rejected: no attributes found",
			},
		}
	}

	allow, reason := staticPolicyEvalutation(op, attrs)
	if !allow {
		slog.Warn("[session]-> Mempool filter: static policy rejected", "op", op, "address", msg.Address, "reason", reason)
		return &packets.CosmosPacket_ResponseMessage{
			ResponseMessage: &packets.ResponseMessage{
				Success: false,
				Message: "CheckTx rejected: " + reason,
			},
		}
	}

	pc := &policy.Context{
		Session:   "",
		Address:   msg.Address,
		Operation: op,
		Resources: map[string]string{
			"count":      "1",
			"complexity": "1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond) // Short timeout per mempool
	defer cancel()

	dec, err := s.dynEval.Evaluate(ctx, pc)
	if err != nil {
		slog.Warn("[session]-> Mempool filter: dynamic evaluator doubtful/error, rejecting tx", "op", op, "address", msg.Address, "err", err)
		return &packets.CosmosPacket_ResponseMessage{
			ResponseMessage: &packets.ResponseMessage{
				Success: false, // Doubtful -> reject in CheckTx
				Message: "CheckTx rejected: service degraded or doubtful",
			},
		}
	}

	slog.Info("[session]-> Mempool filter: passing tx", "op", op, "address", msg.Address, "decision", dec.Allow)
	return &packets.CosmosPacket_ResponseMessage{
		ResponseMessage: &packets.ResponseMessage{
			Success: dec.Allow,
			Message: dec.Message,
		},
	}
}

func staticPolicyEvalutation(op string, attrs *user.Attributes) (bool, string) {
	switch op {
	case "/cosmos.bank.v1beta1.MsgSend":
		canSend := false
		for _, role := range attrs.Roles {
			if role == "office_manager" {
				if attrs.Perms["portfolio.transaction.send"] {
					canSend = true
				}
			}
		}
		if canSend {
			return true, "permission granted"
		}
	}
	return true, "allowed by static policy but missing rule (failOpen true)" // you can choos to apply failOpen policy even here
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

// Parents -> Server -> Session
type handler struct{}

func newHandler() actor.Receiver {
	return &handler{}
}

func (handler) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		slog.Info("[handler]-> started with PID: %v", "pid", c.PID())
	case actor.Stopped:
		for i := 0; i < 1; i++ {
			slog.Info("\r[handler]-> stopping in %d", "i", 1-i)
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
			slog.Info("[handler]-> received auth message:", "message", m)
			c.Send(c.Parent(), packet)
		default:
			slog.Info("[handler]-> unrecognized message type in CosmosPacket:", slog.Any("type", fmt.Sprintf("%T", m)))
		}

	}
}
