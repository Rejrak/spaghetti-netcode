package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"reflect"
	"spaghetti/server/pkg/packets"
	"sync"
	"syscall"
	"time"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/protobuf/proto"
)

// TODO 1 Aggiungere un campo "tipo" (o "opcode") al messaggio wrapper:
// In questo modo, puoi includere un enum o una stringa che indichi esplicitamente il tipo di messaggio. Il ricevente legge questo campo e decide quale struttura utilizzare per l'unmarshal.
// OPPURE
// Utilizzare il tipo google.protobuf.Any:
// Con il tipo Any puoi incapsulare messaggi arbitrari. Il campo Any contiene un campo type_url che specifica il tipo del messaggio incapsulato, permettendoti di usare poi la funzione di unpacking per ottenere il tipo corretto.

type handler struct{}

func newHandler() actor.Receiver {
	return &handler{}
}

func (handler) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case []byte:
		packet := &packets.Packet{}
		err := proto.Unmarshal(msg, packet)
		if err != nil {
			slog.Info("error unmarshalling data: %v", err)
		}
		switch x := packet.Msg.(type) {
		case *packets.Packet_Chat:
			// Il messaggio è di tipo ChatMessage, puoi accedere a x.Chat
			fmt.Println("Received chat message:", x.Chat.Msg)
		case *packets.Packet_Id:
			// Il messaggio è di tipo IdMessage, puoi accedere a x.Id
			fmt.Println("Received id message:", x.Id.Id)
		default:
			fmt.Println("Tipo di messaggio non riconosciuto")
		}
		c.Send(c.Parent(), packet)
	case actor.Stopped:
		for i := 0; i < 3; i++ {
			fmt.Printf("\r handler stopping in %d", 3-i)
			time.Sleep(time.Second)
		}
		fmt.Println("handler stopped")
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
	switch c.Message().(type) {
	case *packets.Packet:
		slog.Info("Sending packet back")

	case actor.Initialized:
	case actor.Started:
		slog.Info("new connection", "addr", s.conn.RemoteAddr())
		go s.readLoop(c)
	case actor.Stopped:
		s.conn.Close()
	}
}

func (s *session) readLoop(c *actor.Context) {
	buf := make([]byte, 1024)
	var dataBuffer []byte
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
			c.Send(c.Parent().Child("handler/default"), packetBytes)
			dataBuffer = dataBuffer[4+msgLen:]
		}
	}
	c.Send(c.Parent(), &connRem{pid: c.PID()})
}

type connAdd struct {
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

func newServer(listenAddr string) actor.Producer {
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
	if data, ok := c.Message().([]byte); ok {
		fmt.Printf("🔹 Decodifica stringa: %q\n", string(data))
		fmt.Printf("🔹 Dati in esadecimale: %s\n", hex.EncodeToString(data))
	}
	fmt.Printf("🔹 Dettagli reflection: %+v\n", reflect.ValueOf(c.Message()))

	switch msg := c.Message().(type) {
	case actor.Initialized:
		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			panic(err)
		}
		s.ln = ln
		c.SpawnChild(newHandler, "handler", actor.WithID("default"))
	
	case *packets.Packet:
		s.mutex.Lock()
		defer s.mutex.Unlock()
		fmt.Printf("🔹 Ricevuto pacchetto: %+v\n", msg)
		
		data, err := packets.ToBytes(msg)
		if err != nil {
			slog.Error("failed to serialize packet", slog.Attr{Key: "err", Value: slog.AnyValue(err)})
			return
		}
		// Prepend the length of the data as a 4-byte prefix
		dataLen := len(data)
		prefix := []byte{
			byte(dataLen >> 24),
			byte(dataLen >> 16),
			byte(dataLen >> 8),
			byte(dataLen),
		}
		data = append(prefix, data...)
		for pid, conn := range s.sessions {
			fmt.Printf("🔹 Invio pacchetto a: %s\n connessione %v", pid, conn)
			_, err := conn.Write(data)
			if err != nil {
				slog.Error("failed to write to connection", "err", err)
			}
		}

	case actor.Started:
		slog.Info("server started", "addr", s.listenAddr)
		go s.acceptLoop(c)
	case actor.Stopped:
		// on stop all the childs sessions will automatically get the stop
		// message and close all their underlying connection.
	case *connAdd:
		slog.Debug("added new connection to my map", "addr", msg.conn.RemoteAddr(), "pid", msg.pid)
		s.sessions[msg.pid] = msg.conn
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
		pid := c.SpawnChild(newSession(conn), "session", actor.WithID(conn.RemoteAddr().String()))
		c.Send(c.PID(), &connAdd{
			pid:  pid,
			conn: conn,
		})
	}
}

func main() {
	listenAddr := flag.String("listenaddr", ":6000", "listen address of the TCP server")

	e, err := actor.NewEngine(actor.NewEngineConfig())
	if err != nil {
		panic(err)
	}
	serverPID := e.Spawn(newServer(*listenAddr), "server")

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
	<-sigch

	<-e.Poison(serverPID).Done()
}
