package server

import (
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"math/rand"
	"net"
	configactors "spaghetti/internal/actors/config"
	"spaghetti/internal/actors/gossipactor"
	"spaghetti/internal/actors/synchronizer"
	"spaghetti/internal/configcluster"
	"spaghetti/internal/gossip"
	"spaghetti/internal/remote/policy"
	"spaghetti/internal/utils/cache"
	"strconv"
	"time"

	"github.com/anthdm/hollywood/actor"
	"google.golang.org/grpc"
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
	listenAddr     string
	ln             net.Listener
	sessions       map[*actor.PID]net.Conn
	configPID      *actor.PID
	gossipAddr     string
	gossipPeers    []gossipactor.PeerInfo
	gossipServer   *grpc.Server
	gossipListener net.Listener
	localNodeID    configcluster.NodeID
	// mutex      sync.Mutex
}

func NewServer(listenAddr string, gossipAddr string, peers []gossipactor.PeerInfo) actor.Producer {
	return func() actor.Receiver {
		return &server{
			listenAddr:  listenAddr,
			sessions:    make(map[*actor.PID]net.Conn),
			gossipAddr:  gossipAddr,
			gossipPeers: peers,
		}
	}
}

func (s *server) startSyncronizer(c *actor.Context) {
	if s.configPID == nil {
		slog.Error("[server]-> missing config actor pid")
		return
	}
	reply := make(chan configcluster.ConfigSnapshot, 1)
	c.Send(s.configPID, &configactors.GetConfig{Reply: reply})
	snapshot := <-reply
	cfgData := snapshot.Data

	cfg := synchronizer.Config{
		DBPath:       cfgData.DBPath,
		PollInterval: 15 * time.Second,
		StaleAfter:   30 * time.Second,
		MaxBatch:     200,

		RemoteTimeout: 3 * time.Second,

		// Keycloak
		KeycloakBaseURL:             cfgData.KeycloakBaseURL,
		KeycloakRealm:               cfgData.KeycloakRealm,
		KeycloakClientID:            cfgData.KeycloakClientID,
		KeycloakClientSecret:        cfgData.KeycloakClientSecret,
		KeycloakEnableWalletLookup:  cfgData.KeycloakEnableWalletLookup,
		KeycloakWalletAttributeName: cfgData.KeycloakWalletAttributeName,
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
		initialCfg := configcluster.ConfigSnapshot{
			Version: 1,
			Data: configcluster.ClusterConfig{
				DBPath: "./authblock.db",

				KeycloakBaseURL:             "http://localhost:8080",
				KeycloakRealm:               "cosmos",
				KeycloakClientID:            "spaghetti-service",
				KeycloakClientSecret:        "nA3XmI7wgHnxdXepKGgMkJz66tyUbviJ",
				KeycloakWalletAttributeName: "",
				KeycloakEnableWalletLookup:  true,

				AttributesBaseURL: "http://localhost:8000",
			},
		}
		initialCfg.Hash = initialCfg.ComputeHash()

		nodeID := configcluster.NodeID("node-" + s.listenAddr)
		s.localNodeID = nodeID
		cfgProps := configactors.NewConfigActor(initialCfg, nodeID)
		s.configPID = c.SpawnChild(cfgProps, "config")

		if s.gossipAddr != "" {
			server, listener, err := gossip.ServeConfigSync(s.gossipAddr, c.Engine(), s.configPID, s.localNodeID)
			if err != nil {
				slog.Error("[server]-> gossip server start failed", "err", err)
			} else {
				s.gossipServer = server
				s.gossipListener = listener
				slog.Info("[server]-> gossip server started", "addr", s.gossipAddr)
			}
		}
		if len(s.gossipPeers) > 0 {
			gossipProps := gossipactor.NewGossipActor(s.localNodeID, s.configPID, s.gossipPeers)
			c.SpawnChild(gossipProps, "gossip")
		}

		s.startSyncronizer(c)
		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			panic(err)
		}
		s.ln = ln
		slog.Info("[server]-> server started", "addr", s.listenAddr)
		go s.acceptLoop(c)
	case actor.Stopped:
		if s.gossipServer != nil {
			s.gossipServer.GracefulStop()
		}
		if s.gossipListener != nil {
			_ = s.gossipListener.Close()
		}
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
	reply := make(chan configcluster.ConfigSnapshot, 1)
	c.Send(s.configPID, &configactors.GetConfig{Reply: reply})
	snapshot := <-reply
	dynEval := initAttributesDynamicEvaluator(snapshot.Data.AttributesBaseURL)
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

func initAttributesDynamicEvaluator(baseURL string) (dynEval *policy.DynamicEvaluator) {
	if baseURL == "" {
		baseURL = "http://localhost:8000"
	}
	dynClient := policy.NewAttributesClient(baseURL, 0, 500*time.Millisecond, 250*time.Millisecond)

	dynEval = &policy.DynamicEvaluator{
		Client:   dynClient,
		Timeout:  250 * time.Millisecond,
		FailOpen: true,
		Cache:    cache.NewTTLCache[policy.Decision](time.Minute),
	}
	return
}
