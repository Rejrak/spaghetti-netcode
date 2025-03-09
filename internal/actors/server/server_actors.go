package server

import (
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"spaghetti/internal/pkg/packets"
	"strconv"
	"sync"
	"time"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/protobuf/proto"
)

type handler struct{}

func newHandler() actor.Receiver {
	return &handler{}
}

func (handler) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		fmt.Printf("\nHandler started with PID: %v", c.PID())
	case actor.Stopped:
		for i := 0; i < 3; i++ {
			fmt.Printf("\r handler stopping in %d", 3-i)
			time.Sleep(time.Second)
		}
		fmt.Println("\nhandler stopped")
	case []byte:
		packet := &packets.Packet{}
		err := proto.Unmarshal(msg, packet)
		if err != nil {
			slog.Info("\nerror unmarshalling data: %v", err)
		}
		switch pckg := packet.Msg.(type) {
		case *packets.Packet_Chat:
			fmt.Println("\nReceived chat message:", pckg.Chat.Msg)
			c.Send(c.Parent(), pckg)
		case *packets.Packet_Position:
			fmt.Println("\nReceived Position message:", pckg.Position)
			c.Send(c.Parent(), pckg)
		default:
			fmt.Println("\nTipo di messaggio non riconosciuto")
		}

	}
}

type session struct {
	conn net.Conn
}

func newSession(conn net.Conn) actor.Producer {
	return func() actor.Receiver {
		return &session{
			conn: conn,
		}
	}
}

func (s *session) Receive(c *actor.Context) {

	switch msg := c.Message().(type) {
	case actor.Started:
		c.SpawnChild(newHandler, "handler", actor.WithID("session"))
		slog.Info("new connection", "addr", s.conn.RemoteAddr())
		go s.readLoop(c)
	case actor.Stopped:
		s.conn.Close()
	case *packets.Packet_Chat:
		switch msg.Chat.Msg {
		case "toggle_torch":
			slog.Info("Sending packet back")
			var packet = &packets.Packet{Msg: msg, SenderId: c.PID().ID}
			data, err := packets.ToBytes(packet)
			if err != nil {
				slog.Error("failed to serialize packet", slog.Attr{Key: "err", Value: slog.AnyValue(err)})
				return
			}
			if _, err := s.conn.Write(data); err != nil {
				panic(err)
			}
		default:
			slog.Error("unrecognized command")
		}

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
}

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
	mutex      sync.Mutex
}

func NewServer(listenAddr string) actor.Producer {
	return func() actor.Receiver {
		return &server{
			listenAddr: listenAddr,
			sessions:   make(map[*actor.PID]net.Conn),
		}
	}
}

func (s *server) Receive(c *actor.Context) {
	fmt.Printf("🔹 Ricevuto messaggio di tipo: %T\n", c.Message())
	fmt.Printf("🔹 Valore messaggio: %+v\n", c.Message())

	switch msg := c.Message().(type) {
	case actor.Started:
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
