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
	maxRecentSessions = 3
)

// Memory is a single durable memory extracted from a chat session.
type Memory struct {
	SessionUUID string `json:"session_uuid,omitempty"` // UUID of the session this memory was extracted from
	ID          int    `json:"id"`
	Importance  int    `json:"importance"` // 1-10
	Text        string `json:"text"`
}

// UserData is the on-disk representation of a user's state.
type UserData struct {
	UserDescription string   `json:"user_description"`
	Sessions        []string `json:"sessions,omitempty"` // UUIDs of the last maxRecentSessions sessions that produced memories (most recent last)
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

// AddMemories appends new memories, tagging each with the session UUID it was
// extracted from and assigning a monotonic ID.
func (ud *UserData) AddMemories(in []Memory, sessionUUID string) {
	for _, m := range in {
		m.SessionUUID = sessionUUID
		m.ID = ud.NextMemoryID()
		if m.Importance < 1 {
			m.Importance = 1
		}
		if m.Importance > 10 {
			m.Importance = 10
		}
		ud.Memories = append(ud.Memories, m)
	}
}

// AddSession records a session UUID that produced memories, keeping only the
// last maxRecentSessions entries (most recent last). This list drives the
// fresh-context priority in memory selection.
func (ud *UserData) AddSession(sessionUUID string) {
	ud.Sessions = append(ud.Sessions, sessionUUID)
	if len(ud.Sessions) > maxRecentSessions {
		ud.Sessions = ud.Sessions[len(ud.Sessions)-maxRecentSessions:]
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
