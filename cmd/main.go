package main

import (
	"flag"
	"os"
	"os/signal"
	"spaghetti/internal/actors/server"
	"syscall"

	"github.com/anthdm/hollywood/actor"
)

func main() {
	listenAddr := flag.String("listenaddr", ":6000", "listen address of the TCP server")
	e, err := actor.NewEngine(actor.NewEngineConfig())
	if err != nil {
		panic(err)
	}

	serverPID := e.Spawn(server.NewServer(*listenAddr), "server")

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
	<-sigch

	<-e.Poison(serverPID).Done()
}
