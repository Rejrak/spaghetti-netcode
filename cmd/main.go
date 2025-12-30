package main

import (
	"flag"
	"log"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"spaghetti/internal/actors/server"
	"syscall"
	"time"

	"github.com/anthdm/hollywood/actor"
)

func startMemLogger() {
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()

		var ms runtime.MemStats
		for range t.C {
			runtime.ReadMemStats(&ms)
			log.Printf("memstats heap_alloc=%d heap_inuse=%d heap_sys=%d stack_inuse=%d sys=%d num_gc=%d",
				ms.HeapAlloc, ms.HeapInuse, ms.HeapSys, ms.StackInuse, ms.Sys, ms.NumGC,
			)
		}
	}()
}

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
