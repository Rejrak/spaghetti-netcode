package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"spaghetti/internal/user"
)

const (
	subjectA = "cosmos1fl48vsnmsdzcv85q5d2q4z5ajdha8yu34mf0eh"
	subjectB = "cosmos1f9xjhxm0plzrh9cskf4qee4pc2xwp0n0556gh0"
)

func TestManagedSubjectStore(t *testing.T) {
	repo, err := Open(filepath.Join(t.TempDir(), "managed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()

	if err := repo.EnsureAddress(ctx, subjectB, "session-b"); err != nil {
		t.Fatal(err)
	}
	attrs := &user.Attributes{
		CanRead: true, Roles: []string{"role-b"}, Perms: map[string]bool{"supply.transaction.send": true},
	}
	if err := repo.UpsertAttrsExtended(ctx, subjectB, attrs); err != nil {
		t.Fatal(err)
	}
	before, updatedBefore, found, err := repo.GetAttrsExtended(ctx, subjectB)
	if err != nil || !found {
		t.Fatalf("GetAttrsExtended() found/error = %v/%v", found, err)
	}
	var sessionBefore string
	if err := repo.db.QueryRowContext(ctx, `SELECT session FROM users WHERE address=?`, subjectB).Scan(&sessionBefore); err != nil {
		t.Fatal(err)
	}

	for _, subject := range []string{subjectB, subjectA, subjectB} {
		if err := repo.EnsureManagedSubject(ctx, subject); err != nil {
			t.Fatal(err)
		}
	}
	subjects, err := repo.ListManagedSubjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{subjectB, subjectA}; !reflect.DeepEqual(subjects, want) {
		t.Fatalf("subjects = %v, want %v", subjects, want)
	}
	after, updatedAfter, found, err := repo.GetAttrsExtended(ctx, subjectB)
	if err != nil || !found {
		t.Fatalf("GetAttrsExtended() found/error = %v/%v", found, err)
	}
	var sessionAfter string
	if err := repo.db.QueryRowContext(ctx, `SELECT session FROM users WHERE address=?`, subjectB).Scan(&sessionAfter); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) || updatedAfter != updatedBefore || sessionAfter != sessionBefore {
		t.Fatalf("registration changed existing state: attrs=%+v/%+v updated=%d/%d session=%q/%q", before, after, updatedBefore, updatedAfter, sessionBefore, sessionAfter)
	}
	if err := repo.EnsureManagedSubject(ctx, "not-a-cosmos-address"); err == nil {
		t.Fatal("invalid managed subject accepted")
	}
	var count int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE address=?`, subjectB).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotent row count/error = %d/%v", count, err)
	}
}
