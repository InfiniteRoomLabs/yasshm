package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

const (
	tA tenant.TenantID = "tenant-a"
	tB tenant.TenantID = "tenant-b"
)

var t0 = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func id(n int) tenant.CommandID { return tenant.CommandID(fmt.Sprintf("%032x", n)) }

// privDir returns a new 0700 directory (t.TempDir gives 0755).
func privDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory needs the x bit; 0700 is the mode under test
		t.Fatal(err)
	}
	return dir
}

func openT(ctx context.Context, t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.WithoutCancel(ctx)) })
	return s
}

func rec(tid tenant.TenantID, n int, at time.Time) *Record {
	return &Record{
		Tenant: tid, ID: id(n), Principal: "user:a@example.net", RequestID: "req-1", Server: "srv-1",
		Command: "echo hi", Status: StatusRunning, CreatedAt: at, MergeStderr: true, TimeoutSeconds: 60,
	}
}

func insert(ctx context.Context, t *testing.T, s *Store, r *Record) {
	t.Helper()
	if err := s.Insert(ctx, r.Tenant, r); err != nil {
		t.Fatal(err)
	}
}

func finish(ctx context.Context, t *testing.T, s *Store, tid tenant.TenantID, n int, st Status, at time.Time, stored int64) {
	t.Helper()
	if err := s.Finish(ctx, tid, id(n), &Final{Status: st, FinishedAt: at, StoredBytes: stored}); err != nil {
		t.Fatal(err)
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	t.Run("happy/creates a private database in WAL mode at the current version", func(t *testing.T) {
		dir := privDir(t)
		s := openT(ctx, t, dir)
		insert(ctx, t, s, rec(tA, 1, t0)) // makes SQLite create the -wal file
		for _, name := range []string{FileName, FileName + "-wal", FileName + "-shm"} {
			fi, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v, want 0600", name, fi.Mode().Perm())
			}
		}
		var mode string
		if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode %q %v", mode, err)
		}
		var fk, syncMode int
		if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("foreign_keys %d %v", fk, err)
		}
		if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode); err != nil || syncMode != 2 {
			t.Fatalf("synchronous %d %v (want 2, FULL)", syncMode, err)
		}
		if v, err := s.Version(ctx); err != nil || v != SchemaVersion() || v != 1 {
			t.Fatalf("version %d %v", v, err)
		}
	})
	t.Run("happy/reopen is idempotent and keeps the data", func(t *testing.T) {
		dir := privDir(t)
		s, err := Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		insert(ctx, t, s, rec(tA, 1, t0))
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(filepath.Join(dir, FileName+"-wal")); err == nil && fi.Size() != 0 {
			t.Errorf("WAL not checkpointed on close: %d bytes", fi.Size())
		}
		s2 := openT(ctx, t, dir)
		if v, _ := s2.Version(ctx); v != 1 {
			t.Fatalf("version %d", v)
		}
		if _, err := s2.Get(ctx, tA, id(1)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sad/a database newer than the binary is refused and not written", func(t *testing.T) {
		dir := privDir(t)
		p := filepath.Join(dir, FileName)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", "file:"+p)
		if err != nil {
			t.Fatal(err)
		}
		if _, xerr := raw.ExecContext(ctx, "CREATE TABLE future (x INTEGER); PRAGMA user_version = 99"); xerr != nil {
			t.Fatal(xerr)
		}
		if cerr := raw.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		before, err := os.ReadFile(p) //nolint:gosec // G304: a test temp file
		if err != nil {
			t.Fatal(err)
		}
		_, err = Open(ctx, dir)
		if !errors.Is(err, ErrTooNew) {
			t.Fatalf("err = %v, want ErrTooNew", err)
		}
		for _, want := range []string{"99", "1"} {
			if !bytes.Contains([]byte(err.Error()), []byte(want)) {
				t.Errorf("message %q does not name version %s", err, want)
			}
		}
		after, err := os.ReadFile(p) //nolint:gosec // G304: a test temp file
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("the newer database was written")
		}
		if _, err := os.Stat(p + "-wal"); err == nil {
			t.Fatal("a WAL file was created for the newer database")
		}
	})
	t.Run("sad/a group-accessible data directory is refused", func(t *testing.T) {
		dir := privDir(t)
		if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // G302: the test needs the unsafe mode
			t.Fatal(err)
		}
		if _, err := Open(ctx, dir); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
			t.Fatal("database created in an unsafe directory")
		}
	})
	t.Run("sad/a data directory that is a file or missing is refused", func(t *testing.T) {
		f := filepath.Join(privDir(t), "file")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, f); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("file: err = %v", err)
		}
		if _, err := Open(ctx, filepath.Join(privDir(t), "missing")); err == nil {
			t.Fatal("missing directory accepted")
		}
	})
	t.Run("sad/a database file with group or other bits is refused", func(t *testing.T) {
		dir := privDir(t)
		if err := os.WriteFile(filepath.Join(dir, FileName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, FileName), 0o644); err != nil { //nolint:gosec // G302: the test needs the unsafe mode
			t.Fatal(err)
		}
		if _, err := Open(ctx, dir); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("raging/a database path that is a symbolic link or a directory is refused", func(t *testing.T) {
		dir := privDir(t)
		target := filepath.Join(privDir(t), "elsewhere.db")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, dir); err == nil {
			t.Fatal("symbolic link accepted")
		}
		dir2 := privDir(t)
		if err := os.Mkdir(filepath.Join(dir2, FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, dir2); err == nil {
			t.Fatal("directory accepted as the database")
		}
	})
	t.Run("sad/a file that is not a database fails to open", func(t *testing.T) {
		dir := privDir(t)
		if err := os.WriteFile(filepath.Join(dir, FileName), bytes.Repeat([]byte("not sqlite "), 100), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, dir); err == nil {
			t.Fatal("garbage file accepted")
		}
	})
	t.Run("edge/a data directory with URL metacharacters in its name works", func(t *testing.T) {
		dir := filepath.Join(privDir(t), "a?b#c%20d")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		s := openT(ctx, t, dir)
		insert(ctx, t, s, rec(tA, 1, t0))
		if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
			t.Fatalf("database not at the expected path: %v", err)
		}
	})
	t.Run("corner/two stores on one file share it under the busy timeout", func(t *testing.T) {
		dir := privDir(t)
		a, b := openT(ctx, t, dir), openT(ctx, t, dir)
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := a
				if i%2 == 1 {
					s = b
				}
				if err := s.Insert(ctx, tA, rec(tA, i+1, t0.Add(time.Duration(i)*time.Second))); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		got, _, err := a.List(ctx, tA, Filter{Limit: 100})
		if err != nil || len(got) != 20 {
			t.Fatalf("%d records, %v", len(got), err)
		}
	})
}

func TestStateMachine(t *testing.T) {
	all := []Status{StatusRunning, StatusSucceeded, StatusFailed, StatusKilled, StatusTimedOut, StatusLost, StatusError}
	t.Run("happy/running goes to every final status and nothing leaves a final one", func(t *testing.T) {
		for _, from := range all {
			for _, to := range all {
				want := from == StatusRunning && to != StatusRunning
				if CanTransition(from, to) != want {
					t.Errorf("CanTransition(%s, %s) = %v", from, to, !want)
				}
			}
			if !from.Valid() || from.Finished() == (from == StatusRunning) {
				t.Errorf("%s: Valid %v Finished %v", from, from.Valid(), from.Finished())
			}
		}
		if Status("done").Valid() || Status("").Finished() {
			t.Error("an unknown status is valid")
		}
	})
	ctx := context.Background()
	s := openT(ctx, t, privDir(t))
	t.Run("happy/finish records the final fields", func(t *testing.T) {
		insert(ctx, t, s, rec(tA, 1, t0))
		code := 3
		f := &Final{Status: StatusFailed, ExitCode: &code, FinishedAt: t0.Add(time.Second), StdoutBytes: 10, StderrBytes: 5, StoredBytes: 15, Truncated: true, MayStillRun: true}
		if err := s.Finish(ctx, tA, id(1), f); err != nil {
			t.Fatal(err)
		}
		r, err := s.Get(ctx, tA, id(1))
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusFailed || r.ExitCode == nil || *r.ExitCode != 3 || !r.FinishedAt.Equal(t0.Add(time.Second)) ||
			r.StdoutBytes != 10 || r.StderrBytes != 5 || r.StoredBytes != 15 || !r.Truncated || !r.MayStillRun {
			t.Fatalf("record %+v", r)
		}
	})
	t.Run("sad/a finished record never changes status again", func(t *testing.T) {
		for _, st := range []Status{StatusSucceeded, StatusKilled, StatusError} {
			if err := s.Finish(ctx, tA, id(1), &Final{Status: st, FinishedAt: t0}); !errors.Is(err, ErrNotRunning) {
				t.Fatalf("%s: err = %v", st, err)
			}
		}
		if err := s.MarkStarted(ctx, tA, id(1), t0); !errors.Is(err, ErrNotRunning) {
			t.Fatal(err)
		}
		if err := s.MarkKillRequested(ctx, tA, id(1), t0); !errors.Is(err, ErrNotRunning) {
			t.Fatal(err)
		}
		if err := s.MarkMayStillRun(ctx, tA, id(1)); !errors.Is(err, ErrNotRunning) {
			t.Fatal(err)
		}
		if r, _ := s.Get(ctx, tA, id(1)); r.Status != StatusFailed {
			t.Fatalf("status %s", r.Status)
		}
	})
	t.Run("sad/finish refuses lost, running, an unknown status and a zero time", func(t *testing.T) {
		insert(ctx, t, s, rec(tA, 2, t0))
		for _, f := range []Final{{Status: StatusLost, FinishedAt: t0}, {Status: StatusRunning, FinishedAt: t0}, {Status: "done", FinishedAt: t0}, {Status: StatusSucceeded}} {
			if err := s.Finish(ctx, tA, id(2), &f); !errors.Is(err, ErrInvalid) {
				t.Errorf("%+v: err = %v", f, err)
			}
		}
	})
	t.Run("sad/updates of a missing record are not found", func(t *testing.T) {
		if err := s.Finish(ctx, tA, id(99), &Final{Status: StatusSucceeded, FinishedAt: t0}); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if err := s.MarkStarted(ctx, tA, "bad", t0); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	})
	t.Run("happy/start, kill request and may-still-run on a running record", func(t *testing.T) {
		if err := s.MarkStarted(ctx, tA, id(2), t0.Add(time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkKillRequested(ctx, tA, id(2), t0.Add(2*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkMayStillRun(ctx, tA, id(2)); err != nil {
			t.Fatal(err)
		}
		r, _ := s.Get(ctx, tA, id(2))
		if !r.StartedAt.Equal(t0.Add(time.Millisecond)) || !r.KillRequestedAt.Equal(t0.Add(2*time.Millisecond)) || !r.MayStillRun || r.Status != StatusRunning {
			t.Fatalf("%+v", r)
		}
	})
}

func TestInsertAndGet(t *testing.T) {
	ctx := context.Background()
	s := openT(ctx, t, privDir(t))
	t.Run("happy/round trip keeps every field", func(t *testing.T) {
		r := rec(tA, 1, t0.Add(123*time.Microsecond))
		r.Command = "printf 'x\\0y' | wc -c ; echo \"$HOME\" -- ünïcode"
		insert(ctx, t, s, r)
		got, err := s.Get(ctx, tA, id(1))
		if err != nil {
			t.Fatal(err)
		}
		if got.Command != r.Command || got.Principal != r.Principal || got.RequestID != r.RequestID || got.Server != r.Server ||
			!got.CreatedAt.Equal(r.CreatedAt) || got.CreatedAt.Location() != time.UTC || !got.MergeStderr || got.TimeoutSeconds != 60 ||
			got.ExitCode != nil || !got.StartedAt.IsZero() || !got.FinishedAt.IsZero() {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("sad/a duplicate id in the tenant is refused", func(t *testing.T) {
		if err := s.Insert(ctx, tA, rec(tA, 1, t0)); !errors.Is(err, ErrExists) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/invalid records are refused", func(t *testing.T) {
		bad := []func(r *Record){
			func(r *Record) { r.Tenant = tB },
			func(r *Record) { r.Status = StatusSucceeded },
			func(r *Record) { r.Server = "../x" },
			func(r *Record) { r.CreatedAt = time.Time{} },
			func(r *Record) { r.TimeoutSeconds = 0 },
			func(r *Record) { r.ID = "ABC" },
		}
		for i, mut := range bad {
			r := rec(tA, 50+i, t0)
			mut(r)
			if err := s.Insert(ctx, tA, r); !errors.Is(err, ErrInvalid) {
				t.Errorf("case %d: err = %v", i, err)
			}
		}
		if err := s.Insert(ctx, "Bad Tenant", rec("Bad Tenant", 1, t0)); !errors.Is(err, ErrInvalid) {
			t.Error(err)
		}
	})
	t.Run("raging/hostile ids are not found, never an SQL error", func(t *testing.T) {
		for _, bad := range []tenant.CommandID{"", "' OR 1=1 --", "../../etc/passwd", id(1) + "x", "0000000000000000000000000000000G"} {
			if _, err := s.Get(ctx, tA, bad); !errors.Is(err, ErrNotFound) {
				t.Errorf("%q: err = %v", bad, err)
			}
			if ok, err := s.Exists(ctx, tA, bad); ok || err != nil {
				t.Errorf("%q: exists %v %v", bad, ok, err)
			}
		}
		if _, err := s.Get(ctx, "' OR 1=1 --", id(1)); !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
	})
}

func TestTenantIsolation(t *testing.T) {
	ctx := context.Background()
	s := openT(ctx, t, privDir(t))
	insert(ctx, t, s, rec(tA, 1, t0))
	insert(ctx, t, s, rec(tB, 2, t0))
	t.Run("happy/the same id may exist in two tenants", func(t *testing.T) {
		insert(ctx, t, s, rec(tB, 1, t0))
	})
	t.Run("sad/another tenant's record is not found by any method", func(t *testing.T) {
		if _, err := s.Get(ctx, tA, id(2)); !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
		if err := s.MarkStarted(ctx, tA, id(2), t0); !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
		if err := s.Finish(ctx, tA, id(2), &Final{Status: StatusKilled, FinishedAt: t0}); !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
		if err := s.DeleteFinished(ctx, tA, id(2)); !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
		got, _, err := s.List(ctx, tA, Filter{Limit: 100})
		if err != nil || len(got) != 1 || got[0].Tenant != tA {
			t.Fatalf("%+v %v", got, err)
		}
		if r, _ := s.Get(ctx, tB, id(2)); r.Status != StatusRunning {
			t.Fatal("tenant b's record changed")
		}
	})
	t.Run("happy/stored bytes and finished lists are per tenant", func(t *testing.T) {
		finish(ctx, t, s, tA, 1, StatusSucceeded, t0, 100)
		finish(ctx, t, s, tB, 2, StatusSucceeded, t0, 7)
		if n, _ := s.StoredBytes(ctx, tA); n != 100 {
			t.Fatalf("a: %d", n)
		}
		if n, _ := s.StoredBytes(ctx, tB); n != 7 {
			t.Fatalf("b: %d", n)
		}
		got, err := s.OldestFinished(ctx, tA, 10)
		if err != nil || len(got) != 1 || got[0].ID != id(1) || got[0].Tenant != tA {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestList(t *testing.T) {
	ctx := context.Background()
	s := openT(ctx, t, privDir(t))
	// 25 records; 10 and 11 share a creation time, so the id breaks the tie.
	for i := 1; i <= 25; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		if i == 11 {
			at = t0.Add(10 * time.Second)
		}
		r := rec(tA, i, at)
		if i%2 == 0 {
			r.Server = "srv-2"
		}
		insert(ctx, t, s, r)
		if i%3 == 0 {
			finish(ctx, t, s, tA, i, StatusSucceeded, at, 0)
		}
	}
	insert(ctx, t, s, rec(tB, 100, t0.Add(time.Hour)))
	t.Run("happy/pages newest first with no gap and no repeat", func(t *testing.T) {
		var seen []tenant.CommandID
		var after *Position
		for page := 0; ; page++ {
			got, more, err := s.List(ctx, tA, Filter{Limit: 7, After: after})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range got {
				seen = append(seen, r.ID)
			}
			if !more {
				break
			}
			last := got[len(got)-1]
			after = &Position{CreatedAt: last.CreatedAt, ID: last.ID}
			if page > 10 {
				t.Fatal("no end")
			}
		}
		if len(seen) != 25 || seen[0] != id(25) || seen[24] != id(1) {
			t.Fatalf("seen %v", seen)
		}
		// 11 and 10 share a time: the larger id comes first.
		for i, v := range seen {
			if v == id(11) && seen[i+1] != id(10) {
				t.Fatalf("tie order: %v", seen)
			}
		}
	})
	t.Run("happy/filters by server and status", func(t *testing.T) {
		got, more, err := s.List(ctx, tA, Filter{Server: "srv-2", Status: StatusSucceeded, Limit: 100})
		if err != nil || more {
			t.Fatal(err, more)
		}
		for _, r := range got {
			if r.Server != "srv-2" || r.Status != StatusSucceeded {
				t.Fatalf("%+v", r)
			}
		}
		if len(got) != 4 { // 6, 12, 18, 24
			t.Fatalf("%d records", len(got))
		}
	})
	t.Run("sad/invalid filters are refused", func(t *testing.T) {
		for _, f := range []Filter{{Limit: 0}, {Limit: 101}, {Limit: 1, Server: "A B"}, {Limit: 1, Status: "done"},
			{Limit: 1, After: &Position{ID: "x", CreatedAt: t0}}, {Limit: 1, After: &Position{ID: id(1)}}} {
			if _, _, err := s.List(ctx, tA, f); !errors.Is(err, ErrInvalid) {
				t.Errorf("%+v: err = %v", f, err)
			}
		}
		if _, _, err := s.List(ctx, "", Filter{Limit: 1}); !errors.Is(err, ErrInvalid) {
			t.Error(err)
		}
	})
	t.Run("edge/a limit of one and a position past the end", func(t *testing.T) {
		got, more, err := s.List(ctx, tA, Filter{Limit: 1})
		if err != nil || len(got) != 1 || !more {
			t.Fatal(got, more, err)
		}
		got, more, err = s.List(ctx, tA, Filter{Limit: 10, After: &Position{CreatedAt: t0, ID: id(1)}})
		if err != nil || len(got) != 0 || more {
			t.Fatal(got, more, err)
		}
	})
}

func TestRecoverAndJanitorQueries(t *testing.T) {
	ctx := context.Background()
	s := openT(ctx, t, privDir(t))
	insert(ctx, t, s, rec(tA, 1, t0))
	insert(ctx, t, s, rec(tA, 2, t0))
	insert(ctx, t, s, rec(tB, 3, t0))
	finish(ctx, t, s, tA, 2, StatusSucceeded, t0.Add(time.Minute), 5)
	t.Run("happy/recover marks every running record of every tenant lost", func(t *testing.T) {
		at := t0.Add(time.Hour)
		got, err := s.RecoverAllTenants(ctx, at, func(r *Record) (int64, int64) {
			if r.Tenant == tB {
				return 7, -1
			}
			return 3, 4
		})
		if err != nil || len(got) != 2 {
			t.Fatal(got, err)
		}
		for _, r := range got {
			if r.Status != StatusLost || !r.FinishedAt.Equal(at) {
				t.Fatalf("%+v", r)
			}
			stored, _ := s.Get(ctx, r.Tenant, r.ID)
			if stored.Status != StatusLost || !stored.FinishedAt.Equal(at) || stored.StoredBytes != r.StoredBytes || !stored.MayStillRun || !r.MayStillRun {
				t.Fatalf("stored %+v", stored)
			}
		}
		if got[0].Tenant != tA || got[0].StoredBytes != 7 || got[0].StdoutBytes != 3 || got[1].StoredBytes != 7 || got[1].StderrBytes != 0 {
			t.Fatalf("sizes: %+v", got)
		}
		if r, _ := s.Get(ctx, tA, id(2)); r.Status != StatusSucceeded {
			t.Fatal("a finished record was recovered")
		}
		again, err := s.RecoverAllTenants(ctx, at, nil)
		if err != nil || len(again) != 0 {
			t.Fatal(again, err)
		}
	})
	t.Run("happy/finished before, oldest first, and delete", func(t *testing.T) {
		got, err := s.FinishedBefore(ctx, tA, t0.Add(2*time.Minute), 10)
		if err != nil || len(got) != 1 || got[0].ID != id(2) {
			t.Fatal(got, err)
		}
		got, err = s.OldestFinished(ctx, tA, 10)
		if err != nil || len(got) != 2 || got[0].ID != id(2) || got[1].ID != id(1) {
			t.Fatal(got, err)
		}
		if err := s.DeleteFinished(ctx, tA, id(2)); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteFinished(ctx, tA, id(2)); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if ok, _ := s.Exists(ctx, tA, id(2)); ok {
			t.Fatal("still there")
		}
	})
	t.Run("sad/a running record is never deleted or listed as finished", func(t *testing.T) {
		insert(ctx, t, s, rec(tA, 9, t0))
		if err := s.DeleteFinished(ctx, tA, id(9)); !errors.Is(err, ErrRunning) {
			t.Fatal(err)
		}
		got, _ := s.OldestFinished(ctx, tA, 100)
		for _, r := range got {
			if r.Status == StatusRunning {
				t.Fatal("running record listed")
			}
		}
		if _, err := s.FinishedBefore(ctx, tA, t0, 0); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := s.FinishedBefore(ctx, "", t0, 1); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := s.StoredBytes(ctx, ""); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if err := s.DeleteFinished(ctx, tA, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestClosedStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, privDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("sad/every method fails after Close", func(t *testing.T) {
		checks := []error{
			s.Insert(ctx, tA, rec(tA, 1, t0)),
			func() error { _, err := s.Get(ctx, tA, id(1)); return err }(),
			func() error { _, err := s.Exists(ctx, tA, id(1)); return err }(),
			s.MarkStarted(ctx, tA, id(1), t0),
			func() error { _, err := s.RecoverAllTenants(ctx, t0, nil); return err }(),
			func() error { _, _, err := s.List(ctx, tA, Filter{Limit: 1}); return err }(),
			func() error { _, err := s.FinishedBefore(ctx, tA, t0, 1); return err }(),
			func() error { _, err := s.StoredBytes(ctx, tA); return err }(),
			s.DeleteFinished(ctx, tA, id(1)),
			func() error { _, err := s.Version(ctx); return err }(),
		}
		for i, err := range checks {
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Errorf("check %d: err = %v", i, err)
			}
		}
		if err := s.Close(ctx); err == nil {
			t.Error("second Close succeeded")
		}
	})
}
