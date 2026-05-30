package synchronizer

import (
	"context"
	"fmt"
	"spaghetti/internal/remote"
	kc "spaghetti/internal/remote/keycloak"
	"spaghetti/internal/storage/sqlite"
	"spaghetti/internal/user"
	"time"

	"github.com/anthdm/hollywood/actor"
	"golang.org/x/exp/slog"
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
		s.dbg("[sync] started with PID: %v", c.PID())
		s.onStart(c)

	case actor.Stopped:
		s.onStop(c)

	case RegisterAddress:
		s.onRegisterAddress(c, msg)

	case ForceSync:
		s.runSyncOnce(c)
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
	slog.Info("[sync] --> "+format, args...)
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

	if s.cfg.KeycloakBaseURL != "" && s.cfg.KeycloakRealm != "" && s.cfg.KeycloakClientID != "" {
		s.dbg("using Keycloak remote backend (%s / %s)", s.cfg.KeycloakBaseURL, s.cfg.KeycloakRealm)
		s.remote = kc.NewKeycloakClient(kc.KeycloakConfig{
			BaseURL:                     s.cfg.KeycloakBaseURL,
			Realm:                       s.cfg.KeycloakRealm,
			ClientID:                    s.cfg.KeycloakClientID,
			Timeout:                     s.cfg.RemoteTimeout,
			EnableWalletAttributeLookup: s.cfg.KeycloakEnableWalletLookup,
			WalletAttributeName:         s.cfg.KeycloakWalletAttributeName,
		})
	} else {
		s.dbg("no remote backend configured")
	}

	// ticker -> manda Tick a se stesso
	interval := s.cfg.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	s.repeater = c.SendRepeat(c.PID(), Tick{}, interval)
}

func (s *Syncronizer) onStop(c *actor.Context) {
	for i := 0; i < 1; i++ {
		s.dbg("%v stopping in %d", c.PID(), 1-i)
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
	s.syncOne(c, m.Address)
}

func (s *Syncronizer) runSyncOnce(c *actor.Context) {
	type allUsersCap interface {
		FetchAllUsers(ctx context.Context) ([]*user.User, error)
	}
	if au, ok := s.remote.(allUsersCap); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		all, err := au.FetchAllUsers(ctx)
		cancel()
		if err != nil {
			s.dbg("FetchAllUsers error: %v", err)
		} else if len(all) > 0 {
			batchCRUD := make(map[string]*user.Attributes, len(all))

			batchRP := make([]sqlite.RolesPermsRow, 0, len(all))

			for _, u := range all {
				if u == nil || u.Attrs == nil || u.Address == "" {
					continue
				}
				batchRP = append(batchRP, sqlite.RolesPermsRow{
					Address: u.Address,
					Roles:   append([]string(nil), u.Attrs.Roles...),
					Perms:   u.Attrs.Perms,
				})
				s.syncOne(c, u.Address)
			}

			if err := s.repo.UpsertAttrsBatch(context.Background(), batchCRUD); err != nil {
				s.dbg("UpsertAttrsBatch error: %v", err)
			}
			if err := s.repo.ReplaceManyUsersRolesPerms(context.Background(), batchRP); err != nil {
				s.dbg("ReplaceManyUsersRolesPerms error: %v", err)
			} else {
				s.dbg("full sync from remote (batch): n=%d", len(batchRP))
			}
		}
	}

	stale := time.Now().Add(-s.cfg.StaleAfter)
	if s.cfg.StaleAfter <= 0 {
		stale = time.Now().Add(-30 * time.Minute)
		// stale = time.Now().Add(-10 * time.Second)
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
	var attrs *user.Attributes
	var err error

	if s.remote != nil {
		rctx, rcancel := context.WithTimeout(context.Background(), s.cfg.RemoteTimeout)
		attrs, err = s.remote.FetchAttributes(rctx, address)
		// log.Default().Printf("Fetched remote attrs for %s: %v (err=%v)", address, s.fmtAttrs(attrs), err)
		rcancel()
	}

	switch {
	case err == nil && attrs != nil:
		if perr := s.repo.UpsertAttrsExtended(context.Background(), address, attrs); perr != nil {
			s.dbg("UpsertAttrsExtended(%s) error: %v", address, perr)
		} else {
			s.dbg("updated attrs+roles+perms from remote for %s", address)
		}
	default:
		_, _, ok, gerr := s.repo.GetAttrsExtended(context.Background(), address)
		if gerr != nil {
			s.dbg("GetAttrs(%s) error: %v", address, gerr)
			return
		}
		if !ok {
			s.dbg("no local attrs for %s and remote unavailable", address)
			return
		}
		// s.dbg("kept local attrs for %s (remote err: %v, last=%s)", address, err, time.Unix(updatedAt, 0).UTC())
	}
}

func (s *Syncronizer) fmtAttrs(a *user.Attributes) string {
	if a == nil {
		return "<nil>"
	}
	return fmt.Sprintf("C=%v R=%v U=%v D=%v", a.CanCreate, a.CanRead, a.CanUpdate, a.CanDelete)
}
