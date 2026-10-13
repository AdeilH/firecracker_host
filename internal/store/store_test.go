package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestPutGetDelete(t *testing.T) {
	s := openTemp(t)
	rec := VMRecord{ID: "a1", Name: "test", IP: "172.16.100.10", SSHPort: 2200, State: StateRunning, CreatedAt: time.Now().UTC()}
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("a1")
	if !ok || got.Name != "test" || got.State != StateRunning {
		t.Fatalf("get returned %+v ok=%v", got, ok)
	}
	if err := s.UpdateState("a1", StateStopped); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("a1")
	if got.State != StateStopped {
		t.Fatalf("state not updated: %s", got.State)
	}
	if err := s.Delete("a1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a1"); ok {
		t.Fatal("record still present after delete")
	}
	if s.Count() != 0 {
		t.Fatalf("count = %d", s.Count())
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Put(VMRecord{ID: "aa", State: StateRunning, CreatedAt: time.Now().UTC()})
	_ = s1.Put(VMRecord{ID: "bb", State: StateStopped, CreatedAt: time.Now().UTC().Add(time.Second)})

	// reopen from disk
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Count() != 2 {
		t.Fatalf("count after reopen = %d", s2.Count())
	}
	all := s2.All()
	if all[0].ID != "aa" || all[1].ID != "bb" {
		t.Fatalf("records not sorted by creation: %+v", all)
	}

	// state file exists with sane perms
	info, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state.json perms = %v", info.Mode().Perm())
	}
}
