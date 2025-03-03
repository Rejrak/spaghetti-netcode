package main

import (
	"flag"
	"os"
	"os/signal"
	"spaghetti/internal/actors/server"
	"syscall"

	"github.com/anthdm/hollywood/actor"
)

// TODO 1 Aggiungere un campo "tipo" (o "opcode") al messaggio wrapper:
// In questo modo, puoi includere un enum o una stringa che indichi esplicitamente il tipo di messaggio. Il ricevente legge questo campo e decide quale struttura utilizzare per l'unmarshal.
// OPPURE
// Utilizzare il tipo google.protobuf.Any:
// Con il tipo Any puoi incapsulare messaggi arbitrari. Il campo Any contiene un campo type_url che specifica il tipo del messaggio incapsulato, permettendoti di usare poi la funzione di unpacking per ottenere il tipo corretto.

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
