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
	"spaghetti/internal/storage/sqlite"
	"spaghetti/internal/utils/cache"
	"strconv"
	"sync"
	"sync/atomic"
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
	repo       *sqlite.Repo

	stopCh  chan struct{}
	stopped atomic.Bool

	rndMu sync.Mutex
	rnd   *rand.Rand

	dynEval *policy.DynamicEvaluator
}

func NewServer(listenAddr string) actor.Producer {
	repo, err := sqlite.Open("./authblock.db")
	if err != nil {
		slog.Info("[server]-> sqlite open error: %v", "err", err)
		panic(err)
	}

	return func() actor.Receiver {
		return &server{
			listenAddr: listenAddr,
			sessions:   make(map[*actor.PID]net.Conn),
			repo:       repo,
			stopCh:     make(chan struct{}),
			rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
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

		KeycloakBaseURL:            "http://localhost:8080",
		KeycloakRealm:              "cosmos",
		KeycloakClientID:           "spaghetti-service",
		KeycloakClientSecret:       "nA3XmI7wgHnxdXepKGgMkJz66tyUbviJ",
		KeycloakEnableWalletLookup: true,
	}

	props := actor.Producer(func() actor.Receiver {
		return synchronizer.NewSyncronizer(cfg, s.repo)
	})

	pid := c.SpawnChild(props, "syncronizer")
	SyncPID = pid
}

func (s *server) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case string:
		fmt.Printf("[server]-> Ricevuto messaggio di tipo string dal syncronizer: %s\n", msg)

	case actor.Started:
		s.startSyncronizer(c)
		s.dynEval = initAttributesDynamicEvaluator()

		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			panic(err)
		}
		s.ln = ln

		slog.Info("[server]-> server started", "addr", s.listenAddr)

		go s.acceptLoop(c)

	case actor.Stopped:
		s.cleanup()

	case *connAdd:
		if s.stopped.Load() {
			_ = msg.conn.Close()
			return
		}
		slog.Info("[server]-> added new connection to my map", "addr", msg.conn.RemoteAddr(), "pid", msg.pid)
		s.sessions[msg.pid] = msg.conn

	case *connRem:
		if conn, ok := s.sessions[msg.pid]; ok {
			_ = conn.Close()
			delete(s.sessions, msg.pid)
			slog.Debug("[server]-> removed connection from my map (closed)", "pid", msg.pid)
		} else {
			slog.Debug("[server]-> connRem for unknown pid", "pid", msg.pid)
		}

	default:
		slog.Warn("[server]-> unknown message", "msg", msg)
	}
}

func (s *server) cleanup() {
	if s.stopped.Swap(true) {
		return
	}

	close(s.stopCh)

	if s.ln != nil {
		_ = s.ln.Close()
	}

	for pid, conn := range s.sessions {
		_ = conn.Close()
		delete(s.sessions, pid)
	}

	if s.repo != nil {
		type closer interface{ Close() error }
		if c, ok := any(s.repo).(closer); ok {
			_ = c.Close()
		}
	}

	if s.dynEval != nil && s.dynEval.Cache != nil {
		s.dynEval.Cache.Close()
	}

	slog.Info("[server]-> cleanup completed")
}

func (s *server) nextSID() int {
	s.rndMu.Lock()
	defer s.rndMu.Unlock()
	return s.rnd.Intn(math.MaxInt32)
}

func (s *server) acceptLoop(c *actor.Context) {
	dynEval := s.dynEval
	if dynEval == nil {
		dynEval = initAttributesDynamicEvaluator()
		s.dynEval = dynEval
	}

	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		conn, err := s.ln.Accept()
		if err != nil {
			if s.stopped.Load() {
				return
			}
			slog.Error("[server]-> accept error", "err", err)
			return
		}

		if s.stopped.Load() {
			_ = conn.Close()
			return
		}

		sid := s.nextSID()

		session := newSession(conn, dynEval, s.repo)
		actID := actor.WithID("session-" + strconv.Itoa(sid))
		pid := c.SpawnChild(session, "session", actID)

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
