package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"spaghetti/internal/observability"
	"spaghetti/internal/pkg/packets"
	"spaghetti/internal/remote/policy"
	"spaghetti/internal/storage/sqlite"
	"spaghetti/internal/user"
	"time"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/protobuf/proto"
)

var (
	errAttributesNotFound = errors.New("attributes not found")
	errAttributesStale    = errors.New("attributes are stale")
)

// Parents -> Server
type session struct {
	conn      net.Conn
	dbPath    string
	maxAge    time.Duration
	evaluator policy.PolicyEvaluator
	logger    *slog.Logger
}

func newSession(conn net.Conn, dbPath string, maxAge time.Duration, evaluator policy.PolicyEvaluator, logger *slog.Logger) actor.Producer {
	return func() actor.Receiver {
		return &session{
			conn:      conn,
			dbPath:    dbPath,
			maxAge:    maxAge,
			evaluator: evaluator,
			logger:    logger,
		}
	}
}

func (s *session) readUserAttributes(c context.Context, address string) (*user.Attributes, error) {
	repo, err := sqlite.Open(s.dbPath)
	if err != nil {
		slog.Error("[session]-> sqlite open error", "err", err)
		return nil, err
	}
	defer repo.Close()
	userAttrs, updated, ok, err := repo.GetAttrsExtended(c, address)
	if err != nil {
		slog.Error("[session]-> Failed to get user attributes", "err", err)
		return nil, err
	}
	if !ok {
		_ = repo.EnsureAddress(c, address, "")
		return nil, errAttributesNotFound
	}
	if !attributesAreFresh(updated, s.maxAge, time.Now()) {
		return nil, errAttributesStale
	}
	return userAttrs, nil
}

func attributesAreFresh(updated int64, maxAge time.Duration, now time.Time) bool {
	return updated > 0 && maxAge > 0 && now.Sub(time.Unix(updated, 0)) <= maxAge
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

		started := time.Now()
		userAttrs, attrsErr := s.readUserAttributes(c.Context(), auth.Address)
		if errors.Is(attrsErr, errAttributesNotFound) {
			attrsErr = nil
		}
		decision := evaluatePolicy(c.Context(), s.evaluator, policy.PolicyInput{
			Subject:    auth.Address,
			Operation:  auth.Operation,
			Attributes: userAttrs,
		}, attrsErr)
		s.logPolicyDecision(c.Context(), auth.Address, auth.Operation, decision, started)
		response := decisionResponse(decision)
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

func evaluatePolicy(ctx context.Context, evaluator policy.PolicyEvaluator, input policy.PolicyInput, inputErr error) policy.PolicyDecision {
	if errors.Is(inputErr, errAttributesStale) {
		return policy.PolicyDecision{
			ReasonCode: policy.ReasonInputStale,
			Reason:     "normalized attributes are stale",
		}
	}
	if inputErr == nil && evaluator != nil {
		decision, err := evaluator.Evaluate(ctx, input)
		if err == nil && decision.ReasonCode != "" && (!decision.Allow || (decision.PolicyID != "" && decision.PolicyVersion != "")) {
			return decision
		}
		decision.Allow = false
		decision.ReasonCode = policy.ReasonEvaluationError
		decision.Reason = "policy evaluation failed closed"
		return decision
	}
	return policy.PolicyDecision{
		ReasonCode: policy.ReasonEvaluationError,
		Reason:     "policy evaluation failed closed",
	}
}

func decisionResponse(decision policy.PolicyDecision) *packets.CosmosPacket_ResponseMessage {
	return &packets.CosmosPacket_ResponseMessage{
		ResponseMessage: &packets.ResponseMessage{
			Success: decision.Allow,
			Message: decision.ReasonCode + ": " + decision.Reason,
		},
	}
}

func (s *session) logPolicyDecision(ctx context.Context, subject, operation string, decision policy.PolicyDecision, started time.Time) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	outcome := "deny"
	if decision.Allow {
		outcome = "allow"
	}
	sum := sha256.Sum256([]byte(subject))
	logger.InfoContext(ctx, observability.EventPolicyEvaluated,
		"component", "policy",
		"operation", operation,
		"outcome", outcome,
		"reason_code", decision.ReasonCode,
		"policy_id", decision.PolicyID,
		"policy_version", decision.PolicyVersion,
		"subject_hash", hex.EncodeToString(sum[:]),
		"duration_ms", time.Since(started).Milliseconds(),
	)
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
