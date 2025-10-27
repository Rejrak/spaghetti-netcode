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
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
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

CREATE TABLE IF NOT EXISTS user_roles (
  address TEXT NOT NULL,
  role    TEXT NOT NULL,
  PRIMARY KEY(address, role)
);

CREATE TABLE IF NOT EXISTS user_perms (
  address TEXT NOT NULL,
  perm    TEXT NOT NULL,
  value   INTEGER NOT NULL,        -- 0/1
  PRIMARY KEY(address, perm)
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

func (r *Repo) GetAttrsExtended(ctx context.Context, address string) (*user.Attributes, int64, bool, error) {
	// CRUD + updated_at
	row := r.db.QueryRowContext(ctx, `
SELECT can_create, can_read, can_update, can_delete, updated_at
FROM users WHERE address=?`, address)
	var c, rr, u, d int
	var updated int64
	if err := row.Scan(&c, &rr, &u, &d, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	attrs := &user.Attributes{
		CanCreate: c != 0, CanRead: rr != 0, CanUpdate: u != 0, CanDelete: d != 0,
		Perms: map[string]bool{},
	}

	// ROLES
	rs, err := r.db.QueryContext(ctx, `SELECT role FROM user_roles WHERE address=?`, address)
	if err != nil {
		return nil, 0, false, err
	}
	defer rs.Close()
	for rs.Next() {
		var role string
		if err := rs.Scan(&role); err != nil {
			return nil, 0, false, err
		}
		if role != "" {
			attrs.Roles = append(attrs.Roles, role)
		}
	}
	if err := rs.Err(); err != nil {
		return nil, 0, false, err
	}

	// PERMS (solo true salvati)
	ps, err := r.db.QueryContext(ctx, `SELECT perm FROM user_perms WHERE address=? AND value=1`, address)
	if err != nil {
		return nil, 0, false, err
	}
	defer ps.Close()
	for ps.Next() {
		var perm string
		if err := ps.Scan(&perm); err != nil {
			return nil, 0, false, err
		}
		if perm != "" {
			attrs.Perms[perm] = true
		}
	}
	if err := ps.Err(); err != nil {
		return nil, 0, false, err
	}

	return attrs, updated, true, nil
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

func (r *Repo) UpsertAttrsBatch(ctx context.Context, rows map[string]*user.Attributes) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO users(address, can_create, can_read, can_update, can_delete, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(address) DO UPDATE SET
  can_create=excluded.can_create,
  can_read  =excluded.can_read,
  can_update=excluded.can_update,
  can_delete=excluded.can_delete,
  updated_at=excluded.updated_at
`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for addr, attrs := range rows {
		if attrs == nil || addr == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx,
			addr,
			bool2int(attrs.CanCreate),
			bool2int(attrs.CanRead),
			bool2int(attrs.CanUpdate),
			bool2int(attrs.CanDelete),
			now,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (r *Repo) ReplaceUserRolesPerms(ctx context.Context, address string, roles []string, perms map[string]bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// cancella esistenti
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_roles WHERE address=?`, address); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_perms WHERE address=?`, address); err != nil {
		return err
	}

	// inserisci ruoli
	if len(roles) > 0 {
		stmtR, err := tx.PrepareContext(ctx, `INSERT INTO user_roles(address, role) VALUES(?, ?)`)
		if err != nil {
			return err
		}
		for _, role := range roles {
			if role == "" {
				continue
			}
			if _, err := stmtR.ExecContext(ctx, address, role); err != nil {
				_ = stmtR.Close()
				return err
			}
		}
		_ = stmtR.Close()
	}

	if len(perms) > 0 {
		stmtP, err := tx.PrepareContext(ctx, `INSERT INTO user_perms(address, perm, value) VALUES(?, ?, ?)`)
		if err != nil {
			return err
		}
		for p, v := range perms {
			if p == "" {
				continue
			}
			val := 0
			// log.Default().Printf("[repo] --> Setting perm %s = %v for user %s", p, v, address)
			if v {
				val = 1
			}
			if _, err := stmtP.ExecContext(ctx, address, p, val); err != nil {
				_ = stmtP.Close()
				return err
			}
		}
		_ = stmtP.Close()
	}

	return tx.Commit()
}

type RolesPermsRow struct {
	Address string
	Roles   []string
	Perms   map[string]bool
}

func (r *Repo) ReplaceManyUsersRolesPerms(ctx context.Context, rows []RolesPermsRow) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	delRoles, err := tx.PrepareContext(ctx, `DELETE FROM user_roles WHERE address=?`)
	if err != nil {
		return err
	}
	defer delRoles.Close()
	delPerms, err := tx.PrepareContext(ctx, `DELETE FROM user_perms WHERE address=?`)
	if err != nil {
		return err
	}
	defer delPerms.Close()

	insRole, err := tx.PrepareContext(ctx, `INSERT INTO user_roles(address, role) VALUES(?, ?)`)
	if err != nil {
		return err
	}
	defer insRole.Close()

	insPerm, err := tx.PrepareContext(ctx, `INSERT INTO user_perms(address, perm, value) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insPerm.Close()

	for _, row := range rows {
		if row.Address == "" {
			continue
		}

		if _, err = delRoles.ExecContext(ctx, row.Address); err != nil {
			return err
		}
		if _, err = delPerms.ExecContext(ctx, row.Address); err != nil {
			return err
		}

		for _, role := range row.Roles {
			if role == "" {
				continue
			}
			if _, err = insRole.ExecContext(ctx, row.Address, role); err != nil {
				return err
			}
		}
		for p, v := range row.Perms {
			if p == "" {
				continue
			}
			if !v {
				continue
			} // tieni solo true
			if _, err = insPerm.ExecContext(ctx, row.Address, p, 1); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repo) UpsertAttrsExtended(ctx context.Context, address string, attrs *user.Attributes) error {
	if attrs == nil {
		return errors.New("nil attrs")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `
INSERT INTO users(address, can_create, can_read, can_update, can_delete, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(address) DO UPDATE SET
  can_create=excluded.can_create,
  can_read  =excluded.can_read,
  can_update=excluded.can_update,
  can_delete=excluded.can_delete,
  updated_at=excluded.updated_at
`, address, bool2int(attrs.CanCreate), bool2int(attrs.CanRead), bool2int(attrs.CanUpdate), bool2int(attrs.CanDelete), now); err != nil {
		return err
	}

	// roles + perms
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_roles WHERE address=?`, address); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_perms WHERE address=?`, address); err != nil {
		return err
	}

	if len(attrs.Roles) > 0 {
		stmtR, err := tx.PrepareContext(ctx, `INSERT INTO user_roles(address, role) VALUES(?, ?)`)
		if err != nil {
			return err
		}
		for _, role := range attrs.Roles {
			if role == "" {
				continue
			}
			if _, err := stmtR.ExecContext(ctx, address, role); err != nil {
				_ = stmtR.Close()
				return err
			}
		}
		_ = stmtR.Close()
	}
	if len(attrs.Perms) > 0 {
		stmtP, err := tx.PrepareContext(ctx, `INSERT INTO user_perms(address, perm, value) VALUES(?, ?, 1)`)
		if err != nil {
			return err
		}
		for p, v := range attrs.Perms {
			if p == "" || !v {
				continue
			}
			if _, err := stmtP.ExecContext(ctx, address, p); err != nil {
				_ = stmtP.Close()
				return err
			}
		}
		_ = stmtP.Close()
	}

	return tx.Commit()
}

func bool2int(b bool) int {
	if b {
		return 1
	}
	return 0
}
