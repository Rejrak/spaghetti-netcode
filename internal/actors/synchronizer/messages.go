package synchronizer

import "time"

type RegisterAddress struct {
	Address string
	Session string
}

type ForceSync struct{} // chiedi uno sync immediato

type Tick struct{} // tick interno

type Config struct {
	PollInterval  time.Duration // ogni quanto controllare
	StaleAfter    time.Duration // dati locali considerati stantii
	MaxBatch      int           // numero massimo di address per ciclo
	RemoteBaseURL string        // es. http://127.0.0.1:8080
	RemoteTimeout time.Duration // timeout per la HTTP call
	DBPath        string        // path SQLite
	Logf          func(format string, args ...any)
}
