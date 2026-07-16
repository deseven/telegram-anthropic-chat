// Package storage handles loading and persisting per-user JSON data files.
//
// Each file is named {telegram_user_id}.json and contains the user description
// and extracted memories. On every write a timestamped backup is created, up
// to maxBackups; older backups are removed.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/log"
)

const (
	maxBackups = 10
)

// Memory is a single durable memory extracted from a chat session.
type Memory struct {
	ID         int    `json:"id"`
	Importance int    `json:"importance"` // 1-10
	Text       string `json:"text"`
	Date       int64  `json:"date"` // creation time as a Unix timestamp (seconds, UTC)
}

// UserData is the on-disk representation of a user's state.
type UserData struct {
	UserDescription string   `json:"user_description"`
	Memories        []Memory `json:"memories,omitempty"`
}

// Store manages user data files under a base directory.
type Store struct {
	base string
	mu   sync.Mutex
}

// New creates a Store rooted at base, creating the directory if needed.
func New(base string) (*Store, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return &Store{base: base}, nil
}

// Exists reports whether a data file exists for the given user id.
func (s *Store) Exists(userID int64) bool {
	_, err := os.Stat(s.path(userID))
	return err == nil
}

// Load reads the data file for userID. Returns an error if it does not exist.
func (s *Store) Load(userID int64) (*UserData, error) {
	raw, err := os.ReadFile(s.path(userID))
	if err != nil {
		return nil, err
	}
	var ud UserData
	if err := json.Unmarshal(raw, &ud); err != nil {
		return nil, fmt.Errorf("parse user data %d: %w", userID, err)
	}
	if ud.UserDescription == "" {
		return nil, fmt.Errorf("user data %d: user_description is required", userID)
	}
	// Backwards compatibility: memories written before the Date field existed
	// have a zero date. Stamp them with the start of today (UTC) so every
	// memory carries a creation time.
	today := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	for i := range ud.Memories {
		if ud.Memories[i].Date == 0 {
			ud.Memories[i].Date = today
		}
	}
	return &ud, nil
}

// Save writes the data file for userID, creating a timestamped backup first
// and pruning old backups beyond maxBackups.
func (s *Store) Save(userID int64, ud *UserData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.backup(userID); err != nil {
		log.Print("storage", "backup failed for user %d: %v", userID, err)
	}

	raw, err := json.MarshalIndent(ud, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal user data %d: %w", userID, err)
	}
	if err := os.WriteFile(s.path(userID), raw, 0o644); err != nil {
		return fmt.Errorf("write user data %d: %w", userID, err)
	}
	return nil
}

// NextMemoryID returns the next integer memory id for a user (max existing +1),
// or 1 if there are no memories yet.
func (ud *UserData) NextMemoryID() int {
	max := 0
	for _, m := range ud.Memories {
		if m.ID > max {
			max = m.ID
		}
	}
	return max + 1
}

// AddMemories appends new memories, assigning a monotonic ID and stamping the
// creation time. Importance is clamped to the 1-10 range.
func (ud *UserData) AddMemories(in []Memory) {
	now := time.Now().UTC().Unix()
	for _, m := range in {
		m.ID = ud.NextMemoryID()
		if m.Importance < 1 {
			m.Importance = 1
		}
		if m.Importance > 10 {
			m.Importance = 10
		}
		if m.Date == 0 {
			m.Date = now
		}
		ud.Memories = append(ud.Memories, m)
	}
}

// DeleteMemory removes the memory with the given id, if it exists. It returns
// true when a memory was removed, false when no memory matched the id. Memory
// ids of remaining memories are NOT renumbered: ids are stable identifiers.
func (ud *UserData) DeleteMemory(id int) bool {
	for i, m := range ud.Memories {
		if m.ID == id {
			ud.Memories = append(ud.Memories[:i], ud.Memories[i+1:]...)
			return true
		}
	}
	return false
}

// DeleteMemories removes every memory whose id is present in ids. It returns
// the number of memories removed. Memory ids of remaining memories are NOT
// renumbered: ids are stable identifiers. An empty (or nil) ids set is a no-op.
func (ud *UserData) DeleteMemories(ids map[int]bool) int {
	if len(ids) == 0 {
		return 0
	}
	kept := ud.Memories[:0]
	n := 0
	for _, m := range ud.Memories {
		if ids[m.ID] {
			n++
			continue
		}
		kept = append(kept, m)
	}
	ud.Memories = kept
	return n
}

func (s *Store) path(userID int64) string {
	return filepath.Join(s.base, fmt.Sprintf("%d.json", userID))
}

func (s *Store) backup(userID int64) error {
	src := s.path(userID)
	if _, err := os.Stat(src); err != nil {
		return nil // nothing to back up
	}
	ts := time.Now().Format("20060102-150405")
	dst := filepath.Join(s.base, fmt.Sprintf("%d.%s.backup", userID, ts))
	if err := copyFile(src, dst); err != nil {
		return err
	}
	s.pruneBackups(userID)
	return nil
}

func (s *Store) pruneBackups(userID int64) {
	prefix := fmt.Sprintf("%d.", userID)
	entries, err := os.ReadDir(s.base)
	if err != nil {
		return
	}
	var backups []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".backup") {
			backups = append(backups, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(backups)))
	if len(backups) <= maxBackups {
		return
	}
	for _, name := range backups[maxBackups:] {
		os.Remove(filepath.Join(s.base, name))
	}
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}
