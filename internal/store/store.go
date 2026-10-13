// Package store persists the VM inventory as a single atomically-written
// JSON file under the data directory.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// VM states.
const (
	StateCreating = "creating"
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateError    = "error"
)

// VMRecord is the persisted state of one microVM.
type VMRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	MAC       string    `json:"mac"`
	IP        string    `json:"ip"`
	SSHPort   int       `json:"ssh_port"`
	VCPUs     int       `json:"vcpus"`
	MemMib    int       `json:"mem_mib"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	// SSHPubKey is the public key installed in the VM's authorized_keys.
	SSHPubKey string `json:"ssh_pub_key,omitempty"`
	// KeyGenerated is true when the daemon generated the keypair and
	// returned the private half to the caller at creation time.
	KeyGenerated bool `json:"key_generated,omitempty"`
}

type diskFormat struct {
	VMs []VMRecord `json:"vms"`
}

// Store is a concurrency-safe JSON-file-backed VM inventory.
type Store struct {
	mu   sync.Mutex
	path string
	vms  map[string]*VMRecord
}

// Open loads (or initializes) the store at dataDir/state.json.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{
		path: filepath.Join(dataDir, "state.json"),
		vms:  map[string]*VMRecord{},
	}
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, s.persistLocked()
	}
	if err != nil {
		return nil, err
	}
	var df diskFormat
	if err := json.Unmarshal(raw, &df); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	for i := range df.VMs {
		r := df.VMs[i]
		s.vms[r.ID] = &r
	}
	return s, nil
}

// All returns all records sorted by creation time.
func (s *Store) All() []VMRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]VMRecord, 0, len(s.vms))
	for _, r := range s.vms {
		out = append(out, *r)
	}
	sortByCreated(out)
	return out
}

// Get returns a copy of one record.
func (s *Store) Get(id string) (VMRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.vms[id]
	if !ok {
		return VMRecord{}, false
	}
	return *r, true
}

// Put upserts a record and persists.
func (s *Store) Put(rec VMRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := rec
	s.vms[rec.ID] = &cp
	return s.persistLocked()
}

// UpdateState mutates just the state field of a record.
func (s *Store) UpdateState(id, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.vms[id]
	if !ok {
		return fmt.Errorf("vm %s not found", id)
	}
	r.State = state
	return s.persistLocked()
}

// Delete removes a record and persists.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vms, id)
	return s.persistLocked()
}

// Count returns the number of records regardless of state.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.vms)
}

func (s *Store) persistLocked() error {
	list := make([]VMRecord, 0, len(s.vms))
	for _, r := range s.vms {
		list = append(list, *r)
	}
	sortByCreated(list)
	raw, err := json.MarshalIndent(diskFormat{VMs: list}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func sortByCreated(in []VMRecord) {
	sort.Slice(in, func(i, j int) bool { return in[i].CreatedAt.Before(in[j].CreatedAt) })
}
