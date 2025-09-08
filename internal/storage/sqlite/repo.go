package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite" // driver pure Go

	"spaghetti/internal/user"
)

type Repo struct {
	db *sql.DB
}

func Open(path string) (*Repo, error) {
	// PRAGMA per performance e durabilità bilanciata
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	r := &Repo{db: db}
	if err := r.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return r, nil
}

func (r *Repo) Close() error { return r.db.Close() }

func (r *Repo) init(ctx context.Context) error {
	schema := `
CREATE TABLE IF NOT EXISTS users (
  address TEXT PRIMARY KEY,
  session TEXT,
  can_create INTEGER NOT NULL DEFAULT 0,
  can_read   INTEGER NOT NULL DEFAULT 0,
  can_update INTEGER NOT NULL DEFAULT 0,
  can_delete INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0   -- unix seconds
);

CREATE INDEX IF NOT EXISTS idx_users_updated_at ON users(updated_at);
`
	_, err := r.db.ExecContext(ctx, schema)
	return err
}

// EnsureAddress: crea l'utente se non esiste, senza toccare gli attributi.
func (r *Repo) EnsureAddress(ctx context.Context, address, session string) error {
	now := time.Now().Unix()
	_, err := r.db.ExecContext(ctx, `
INSERT INTO users(address, session, updated_at)
VALUES(?, ?, ?)
ON CONFLICT(address) DO UPDATE SET session=excluded.session
`, address, session, now)
	return err
}

func (r *Repo) UpsertAttrs(ctx context.Context, address string, attrs *user.Attributes) error {
	if attrs == nil {
		return errors.New("nil attrs")
	}
	now := time.Now().Unix()
	_, err := r.db.ExecContext(ctx, `
INSERT INTO users(address, can_create, can_read, can_update, can_delete, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(address) DO UPDATE SET
  can_create=excluded.can_create,
  can_read  =excluded.can_read,
  can_update=excluded.can_update,
  can_delete=excluded.can_delete,
  updated_at=excluded.updated_at
`, address, bool2int(attrs.CanCreate), bool2int(attrs.CanRead), bool2int(attrs.CanUpdate), bool2int(attrs.CanDelete), now)
	return err
}

func (r *Repo) GetAttrs(ctx context.Context, address string) (*user.Attributes, int64, bool, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT can_create, can_read, can_update, can_delete, updated_at
FROM users WHERE address=?
`, address)
	var c, r_, u, d int
	var updated int64
	if err := row.Scan(&c, &r_, &u, &d, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	return &user.Attributes{
		CanCreate: c != 0, CanRead: r_ != 0, CanUpdate: u != 0, CanDelete: d != 0,
	}, updated, true, nil
}

func (r *Repo) ListStaleAddresses(ctx context.Context, staleBefore time.Time, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT address
FROM users
WHERE updated_at < ?
ORDER BY updated_at ASC
LIMIT ?
`, staleBefore.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, rows.Err()
}

func bool2int(b bool) int {
	if b {
		return 1
	}
	return 0
}
