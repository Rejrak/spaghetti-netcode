package synchronizer

import (
	"context"
	"fmt"
	"spaghetti/internal/remote"
	"spaghetti/internal/storage/sqlite"
	"spaghetti/internal/user"
	"time"

	"github.com/anthdm/hollywood/actor"
)

// Parent -> Server
type Syncronizer struct {
	cfg    Config
	repo   *sqlite.Repo
	remote remote.Client

	stopCh chan struct{}

	repeater actor.SendRepeater
}

func (s *Syncronizer) Receive(c *actor.Context) {
	switch msg := c.Message().(type) {
	case actor.Started:
		s.dbg("Syncronizer started with PID: %v", c.PID())
		s.onStart(c)

	case actor.Stopped:
		s.onStop(c)

	case RegisterAddress:
		s.onRegisterAddress(c, msg)

	case ForceSync:
		// s.runSyncOnce(c)
	case Tick:
		s.runSyncOnce(c)

	default:
		s.dbg("Message Received: %v", msg)
	}
}

func NewSyncronizer(cfg Config) actor.Receiver {
	return &Syncronizer{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

func (s *Syncronizer) dbg(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf("[sync] --> "+format, args...)
	}
}

func (s *Syncronizer) onStart(c *actor.Context) {
	s.dbg("started with PID=%v", c.PID())

	repo, err := sqlite.Open(s.cfg.DBPath)
	if err != nil {
		s.dbg("sqlite open error: %v", err)
		// puoi decidere di fermare l’attore con un panic (supervisor lo rimonterà)
		panic(err)
	}
	s.repo = repo

	if s.cfg.RemoteBaseURL != "" {
		s.remote = remote.NewHTTPClient(s.cfg.RemoteBaseURL, s.cfg.RemoteTimeout)
	}

	// ticker → manda Tick a se stesso
	interval := s.cfg.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	s.repeater = c.SendRepeat(c.PID(), Tick{}, interval)
}

func (s *Syncronizer) onStop(c *actor.Context) {
	for i := 0; i < 1; i++ {
		s.dbg("Syncronizer %v stopping in %d", c.PID(), 1-i)
		time.Sleep(time.Second)
	}
	close(s.stopCh)
	if s.repo != nil {
		_ = s.repo.Close()
	}
	s.dbg("stopped")
}

func (s *Syncronizer) onRegisterAddress(c *actor.Context, m RegisterAddress) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.repo.EnsureAddress(ctx, m.Address, m.Session); err != nil {
		s.dbg("EnsureAddress(%s) error: %v", m.Address, err)
		return
	}
	// opzionale: tenta subito un refresh di quell’indirizzo
	s.syncOne(c, m.Address)
}

func (s *Syncronizer) runSyncOnce(c *actor.Context) {
	stale := time.Now().Add(-s.cfg.StaleAfter)
	if s.cfg.StaleAfter <= 0 {
		stale = time.Now().Add(-30 * time.Minute)
	}
	limit := s.cfg.MaxBatch
	if limit <= 0 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addresses, err := s.repo.ListStaleAddresses(ctx, stale, limit)
	if err != nil {
		s.dbg("ListStaleAddresses error: %v", err)
		return
	}
	if len(addresses) == 0 {
		s.dbg("nothing to do (no stale addresses)")
		return
	}

	s.dbg("stale batch n=%d", len(addresses))
	for _, addr := range addresses {
		s.syncOne(c, addr)
	}
}

func (s *Syncronizer) syncOne(c *actor.Context, address string) {
	// 1) prova fetch remoto se configurato
	var attrs *user.Attributes
	var err error

	if s.remote != nil {
		rctx, rcancel := context.WithTimeout(context.Background(), s.cfg.RemoteTimeout)
		attrs, err = s.remote.FetchAttributes(rctx, address)
		rcancel()
	}

	switch {
	case err == nil && attrs != nil:
		// 2) persistiamo subito
		if perr := s.repo.UpsertAttrs(context.Background(), address, attrs); perr != nil {
			s.dbg("UpsertAttrs(%s) error: %v", address, perr)
		} else {
			s.dbg("updated attrs from remote for %s", address)
		}
	default:
		// 3) fallback: resta con dati locali
		_, updatedAt, ok, gerr := s.repo.GetAttrs(context.Background(), address)
		if gerr != nil {
			s.dbg("GetAttrs(%s) error: %v", address, gerr)
			return
		}
		if !ok {
			// utente non presente: lo lasciamo senza attrs, verrà ripreso al prossimo giro
			s.dbg("no local attrs for %s and remote unavailable", address)
			return
		}
		// dati esistenti: li teniamo (eventual consistency)
		s.dbg("kept local attrs for %s (remote err: %v, last=%s)", address, err, time.Unix(updatedAt, 0).UTC())
	}
}

func (s *Syncronizer) fmtAttrs(a *user.Attributes) string {
	if a == nil {
		return "<nil>"
	}
	return fmt.Sprintf("C=%v R=%v U=%v D=%v", a.CanCreate, a.CanRead, a.CanUpdate, a.CanDelete)
}
