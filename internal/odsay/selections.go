package odsay

import (
	"context"
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

// SelectionStore persists only normalized server-owned paths, never credentials.
type SelectionStore interface {
	Put(context.Context, string, Selection, time.Time) error
	Fetch(context.Context, string) (Selection, bool, error)
	Recent(context.Context) ([]Selection, error)
}

type Selections struct {
	store   SelectionStore
	mu      sync.Mutex
	entries map[string]selectionEntry
	now     func() time.Time
}

func NewSelections() *Selections {
	return &Selections{entries: map[string]selectionEntry{}, now: time.Now}
}
func NewPersistentSelections(store SelectionStore) *Selections {
	s := NewSelections()
	s.store = store
	return s
}
func (s *Selections) Save(raw Response, q Query, origin string, captured time.Time) (string, time.Time, error) {
	return s.SaveContext(context.Background(), raw, q, origin, captured)
}
func (s *Selections) SaveContext(ctx context.Context, raw Response, q Query, origin string, captured time.Time) (string, time.Time, error) {
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
	if s.store != nil {
		expires := s.now().Add(10 * time.Minute)
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if e := s.store.Put(ctx, id, Selection{copy, q, origin, captured}, expires); e != nil {
			return "", time.Time{}, ErrSelectionStore
		}
		return id, expires, nil
	}
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
	value, found, _ := s.GetContext(context.Background(), id)
	return value, found
}
func (s *Selections) GetContext(ctx context.Context, id string) (Selection, bool, error) {
	if s == nil {
		return Selection{}, false, nil
	}
	if s.store != nil {
		if len(id) != 32 {
			return Selection{}, false, nil
		}
		if _, e := hex.DecodeString(id); e != nil {
			return Selection{}, false, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		value, found, e := s.store.Fetch(ctx, id)
		if e != nil {
			return Selection{}, false, ErrSelectionStore
		}
		return value, found, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.entries[id]
	if !ok {
		return Selection{}, false, nil
	}
	if !s.now().Before(v.expires) {
		delete(s.entries, id)
		return Selection{}, false, nil
	}
	return v.Selection, true, nil
}

// Snapshots exposes only recent server-owned responses for exact section reuse.
// It never performs a provider request or changes a selected route.
func (s *Selections) Snapshots() []Selection {
	if s == nil {
		return nil
	}
	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, e := s.store.Recent(ctx)
		if e != nil {
			return nil
		}
		return out
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
