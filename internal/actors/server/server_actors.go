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
		PollInterval:  30 * time.Second,
		StaleAfter:    30 * time.Minute,
		MaxBatch:      200,
		RemoteBaseURL: "http://127.0.0.1:8080",
		RemoteTimeout: 800 * time.Millisecond,
		DBPath:        "./authblock.db",
		Logf: func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		},
	}
	props := actor.Producer(func() actor.Receiver {
		return synchronizer.NewSyncronizer(cfg)
	})
	pid := c.SpawnChild(props, "syncronizer")
	SyncPID = pid
}

func (s *server) Receive(c *actor.Context) {
	fmt.Printf("-> Ricevuto messaggio di tipo: %T\n", c.Message())
	fmt.Printf("-> Valore messaggio: %+v\n", c.Message())

	switch msg := c.Message().(type) {
	case string:
		fmt.Printf("-> Ricevuto messaggio di tipo string dal syncronizer: %s\n", msg)
	case actor.Started:
		s.startSyncronizer(c)
		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			panic(err)
		}
		s.ln = ln
		slog.Info("server started", "addr", s.listenAddr)
		go s.acceptLoop(c)
	case actor.Stopped:
		break
	case *connAdd:
		slog.Info("added new connection to my map", "addr", msg.conn.RemoteAddr(), "pid", msg.pid)
		s.sessions[msg.pid] = msg.conn
		var packet = &packets.Packet{}
		packet.SenderId = msg.pid.ID
		data, err := packets.ToBytes(packet)
		if err != nil {
			slog.Error("Failed to  send init message", err)
		}
		time.Sleep(time.Millisecond * 100)
		msg.conn.Write(data)

	case *connRem:
		slog.Debug("removed connection from my map", "pid", msg.pid)
		delete(s.sessions, msg.pid)
	default:
		slog.Warn("unknown message", "msg", msg)
	}
}

func (s *server) acceptLoop(c *actor.Context) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			slog.Error("accept error", "err", err)
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
