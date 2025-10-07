package server

import (
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"spaghetti/internal/actors/synchronizer"
	"spaghetti/internal/pkg/packets"
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
	fmt.Printf("[server]-> Ricevuto messaggio di tipo: %T\n", c.Message())
	fmt.Printf("[server]-> Valore messaggio: %+v\n", c.Message())

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
		var packet = &packets.Packet{}
		packet.SenderId = msg.pid.ID
		data, err := packets.ToBytes(packet)
		if err != nil {
			slog.Error("[server]-> Failed to  send init message", err)
		}
		time.Sleep(time.Millisecond * 100)
		msg.conn.Write(data)

	case *connRem:
		slog.Debug("[server]-> removed connection from my map", "pid", msg.pid)
		delete(s.sessions, msg.pid)
	default:
		slog.Warn("[server]-> unknown message", "msg", msg)
	}
}

func (s *server) acceptLoop(c *actor.Context) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			slog.Error("[server]-> accept error", "err", err)
			break
		}
		sid := rand.Intn(math.MaxInt)
		pid := c.SpawnChild(newSession(conn), "session", actor.WithID(strconv.Itoa(sid)))
		c.Send(c.PID(), &connAdd{
			sid:  sid,
			pid:  pid,
			conn: conn,
		})
	}
}
