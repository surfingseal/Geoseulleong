package odsay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Selections keeps server-owned provider paths so indices cannot be rebound by
// clients. IDs are unguessable, expire after ten minutes and never trigger a search.
type Selection struct {
	Response   Response
	Query      Query
	Origin     string
	CapturedAt time.Time
}
type selectionEntry struct {
	Selection
	expires time.Time
}
type Selections struct {
	mu      sync.Mutex
	entries map[string]selectionEntry
	now     func() time.Time
}

func NewSelections() *Selections {
	return &Selections{entries: map[string]selectionEntry{}, now: time.Now}
}
func (s *Selections) Save(raw Response, q Query, origin string, captured time.Time) (string, time.Time, error) {
	// Keep an independent immutable copy, not a provider cache's shared slices.
	b, e := json.Marshal(raw)
	if e != nil {
		return "", time.Time{}, e
	}
	var copy Response
	if e = json.Unmarshal(b, &copy); e != nil {
		return "", time.Time{}, e
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return "", time.Time{}, e
	}
	id := hex.EncodeToString(nonce)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, v := range s.entries {
		if !now.Before(v.expires) {
			delete(s.entries, k)
		}
	}
	if len(s.entries) >= 64 {
		oldest := ""
		var expiry time.Time
		for k, v := range s.entries {
			if oldest == "" || v.expires.Before(expiry) {
				oldest = k
				expiry = v.expires
			}
		}
		delete(s.entries, oldest)
	}
	expires := now.Add(10 * time.Minute)
	s.entries[id] = selectionEntry{Selection{copy, q, origin, captured}, expires}
	return id, expires, nil
}
func (s *Selections) Get(id string) (Selection, bool) {
	if s == nil {
		return Selection{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.entries[id]
	if !ok {
		return Selection{}, false
	}
	if !s.now().Before(v.expires) {
		delete(s.entries, id)
		return Selection{}, false
	}
	return v.Selection, true
}

// Snapshots exposes only recent server-owned responses for exact section reuse.
// It never performs a provider request or changes a selected route.
func (s *Selections) Snapshots() []Selection {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Selection{}
	for id, v := range s.entries {
		if !s.now().Before(v.expires) {
			delete(s.entries, id)
			continue
		}
		out = append(out, v.Selection)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CapturedAt.After(out[j].CapturedAt) })
	return out
}
