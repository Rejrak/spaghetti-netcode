package server

import (
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"math/rand"
	"net"
	"spaghetti/internal/actors/synchronizer"
	"spaghetti/internal/remote/policy"
	"spaghetti/internal/utils/cache"
	"strconv"
	"time"

	"github.com/anthdm/hollywood/actor"
)

var SyncPID *actor.PID

type connAdd struct {
	sid  int
	pid  *actor.PID
	conn net.Conn
}

type connRem struct {
	pid *actor.PID
}

type server struct {
	listenAddr string
	ln         net.Listener
	sessions   map[*actor.PID]net.Conn
	// mutex      sync.Mutex
}

func NewServer(listenAddr string) actor.Producer {
	return func() actor.Receiver {
		return &server{
			listenAddr: listenAddr,
			sessions:   make(map[*actor.PID]net.Conn),
		}
	}
}

func (s *server) startSyncronizer(c *actor.Context) {
	cfg := synchronizer.Config{
		DBPath:       "./authblock.db",
		PollInterval: 15 * time.Second,
		StaleAfter:   30 * time.Second,
		MaxBatch:     200,

		RemoteTimeout: 3 * time.Second,

		// Keycloak
		KeycloakBaseURL:            "http://localhost:8080",
		KeycloakRealm:              "cosmos",
		KeycloakClientID:           "spaghetti-service",
		KeycloakClientSecret:       "nA3XmI7wgHnxdXepKGgMkJz66tyUbviJ",
		KeycloakEnableWalletLookup: true,
		// KeycloakWalletAttributeName: "walletAddress", // default già gestito
	}

	props := actor.Producer(func() actor.Receiver {
		return synchronizer.NewSyncronizer(cfg)
	})
	pid := c.SpawnChild(props, "syncronizer")
	SyncPID = pid
}

func (s *server) Receive(c *actor.Context) {
	// fmt.Printf("[server]-> Ricevuto messaggio di tipo: %T\n", c.Message())
	// fmt.Printf("[server]-> Valore messaggio: %+v\n", c.Message())

	switch msg := c.Message().(type) {
	case string:
		fmt.Printf("[server]-> Ricevuto messaggio di tipo string dal syncronizer: %s\n", msg)
	case actor.Started:
		s.startSyncronizer(c)
		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			panic(err)
		}
		s.ln = ln
		slog.Info("[server]-> server started", "addr", s.listenAddr)
		go s.acceptLoop(c)
	case actor.Stopped:
		break
	case *connAdd:
		slog.Info("[server]-> added new connection to my map", "addr", msg.conn.RemoteAddr(), "pid", msg.pid)
		s.sessions[msg.pid] = msg.conn

	case *connRem:
		slog.Debug("[server]-> removed connection from my map", "pid", msg.pid)
		delete(s.sessions, msg.pid)
	default:
		slog.Warn("[server]-> unknown message", "msg", msg)
	}
}

func (s *server) acceptLoop(c *actor.Context) {
	dynEval := initAttributesDynamicEvaluator()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			slog.Error("[server]-> accept error", "err", err)
			break
		}
		sid := rand.Intn(math.MaxInt)
		pid := c.SpawnChild(newSession(conn, dynEval), "session", actor.WithID(strconv.Itoa(sid)))
		c.Send(c.PID(), &connAdd{
			sid:  sid,
			pid:  pid,
			conn: conn,
		})
	}
}

func initCosmosDynamicEvaluator() (dynEval *policy.DynamicEvaluator) {
	min := big.NewInt(30000)
	lcd := "http://127.0.0.1:1317"
	dynClient := policy.NewCosmosBalanceClient(lcd, "token", min, 250*time.Millisecond)

	dynEval = &policy.DynamicEvaluator{
		Client:   dynClient,
		Timeout:  250 * time.Millisecond,
		FailOpen: false,
		Cache:    cache.NewTTLCache[policy.Decision](time.Minute),
	}
	return
}

func initAttributesDynamicEvaluator() (dynEval *policy.DynamicEvaluator) {
	dynClient := policy.NewAttributesClient("http://localhost:8000", 10, 500*time.Millisecond, 250*time.Millisecond)

	dynEval = &policy.DynamicEvaluator{
		Client:   dynClient,
		Timeout:  250 * time.Millisecond,
		FailOpen: true,
		Cache:    cache.NewTTLCache[policy.Decision](time.Minute),
	}
	return
}
