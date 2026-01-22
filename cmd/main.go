package main

import (
	"flag"
	"os"
	"os/signal"
	"spaghetti/internal/actors/gossipactor"
	"spaghetti/internal/actors/server"
	"spaghetti/internal/configcluster"
	"strings"
	"syscall"

	"github.com/anthdm/hollywood/actor"
)

func main() {
	listenAddr := flag.String("listenaddr", ":6000", "listen address of the TCP server")

	gossipAddr := flag.String("gossipaddr", "", "listen address for the gossip gRPC server")
	gossipPeers := flag.String("gossippeers", "", "comma-separated peers (nodeID@host:port or host:port)")
	flag.Parse()

	if *gossipAddr == "" {
		*gossipAddr = os.Getenv("SPAGHETTI_GOSSIP_ADDR")
	}

	if *gossipPeers == "" {
		*gossipPeers = os.Getenv("SPAGHETTI_GOSSIP_PEERS")
	}
	peers := parsePeers(*gossipPeers)

	e, err := actor.NewEngine(actor.NewEngineConfig())
	if err != nil {
		panic(err)
	}

	serverPID := e.Spawn(server.NewServer(*listenAddr, *gossipAddr, peers), "server")

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
	<-sigch

	<-e.Poison(serverPID).Done()
}

func parsePeers(raw string) []gossipactor.PeerInfo {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	peers := make([]gossipactor.PeerInfo, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		nodeID := ""
		addr := part
		if split := strings.SplitN(part, "@", 2); len(split) == 2 {
			nodeID = split[0]
			addr = split[1]
		}
		if addr == "" {
			continue
		}
		if nodeID == "" {
			nodeID = addr
		}
		peers = append(peers, gossipactor.PeerInfo{
			NodeID:  configcluster.NodeID(nodeID),
			Address: addr,
		})
	}
	return peers
}
