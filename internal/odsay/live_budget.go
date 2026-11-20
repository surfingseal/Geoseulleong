package odsay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var ErrState = errors.New("ODsay budget state unavailable")
var ErrTimeout = errors.New("ODsay request timeout")

type LiveConfig struct {
	Approved   bool
	DailyLimit int
	TotalLimit int
	// ExternalReserve conservatively accounts for calls made outside this server each day.
	ExternalReserve int
	CacheTTL        time.Duration
	StateDir        string
	Reserve         func(context.Context) error
}
type budgetState struct {
	Provider  string `json:"provider,omitempty"`
	Version   int    `json:"version"`
	Day       string `json:"day_asia_seoul"`
	DailyUsed int    `json:"daily_reserved_calls"`
	TotalUsed int    `json:"total_reserved_calls"`
}
type cachedRoute struct {
	received time.Time
	response Response
	err      error
	expires  time.Time
}
type routeFlight struct {
	done   chan struct{}
	result cachedRoute
}

// Live is one server-wide provider. A lifetime file lock rejects another process using its ledger.
// It caches only provider routes; database statistics are still queried for each request.
type Live struct {
	provider         string
	mu               sync.Mutex
	cfg              LiveConfig
	upstream         Searcher
	state            budgetState
	lock             *os.File
	now              func() time.Time
	zone             *time.Location
	cache            map[string]cachedRoute
	flights          map[string]*routeFlight
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	closed, poisoned bool
}

func NewLive(key string, cfg LiveConfig) (*Live, error) {
	if !cfg.Approved {
		return nil, ErrDisabled
	}
	if cfg.DailyLimit < 1 || cfg.DailyLimit > 30 || cfg.TotalLimit < 1 || cfg.TotalLimit > 10000 || cfg.ExternalReserve < 0 || cfg.ExternalReserve >= cfg.DailyLimit || cfg.CacheTTL < time.Second || cfg.CacheTTL > time.Hour || (cfg.StateDir == "" && cfg.Reserve == nil) {
		return nil, ErrInput
	}
	// Keep the original single-call CLI constructor unchanged. Only this guarded path gets a larger client budget.
	client, e := NewClient(Config{Key: key, Approved: true, MaxRequests: 1})
	if e != nil {
		return nil, e
	}
	client.max = cfg.TotalLimit
	return newBudgetedProvider(client, cfg, "odsay", 30)
}

// NewBudgetedProvider shares durable reservations/cache with another normalized provider.
// Kakao keeps a distinct provider-tagged ledger; ODsay's original constructor is unchanged.
func NewBudgetedProvider(upstream Searcher, cfg LiveConfig) (*Live, error) {
	return newBudgetedProvider(upstream, cfg, "kakao", 1000)
}

// NewBudgetedODsayProvider applies the existing durable ODsay budget/cache to a
// normalized searcher. Reuse an already-created Live directly instead of nesting guards.
func NewBudgetedODsayProvider(upstream Searcher, cfg LiveConfig) (*Live, error) {
	return newBudgetedProvider(upstream, cfg, "odsay", 30)
}

func newBudgetedProvider(upstream Searcher, cfg LiveConfig, provider string, maxDaily int) (*Live, error) {
	if !cfg.Approved {
		return nil, ErrDisabled
	}
	if upstream == nil || cfg.DailyLimit < 1 || cfg.DailyLimit > maxDaily || cfg.TotalLimit < 1 || cfg.TotalLimit > 10000 || cfg.ExternalReserve < 0 || cfg.ExternalReserve >= cfg.DailyLimit || cfg.CacheTTL < time.Second || cfg.CacheTTL > time.Hour || (cfg.StateDir == "" && cfg.Reserve == nil) {
		return nil, ErrInput
	}
	if cfg.Reserve != nil {
		zone, e := time.LoadLocation("Asia/Seoul")
		if e != nil {
			return nil, ErrState
		}
		ctx, cancel := context.WithCancel(context.Background())
		return &Live{cfg: cfg, provider: provider, upstream: upstream, now: time.Now, zone: zone, cache: map[string]cachedRoute{}, flights: map[string]*routeFlight{}, ctx: ctx, cancel: cancel}, nil
	}
	var e error
	if e = os.MkdirAll(cfg.StateDir, 0700); e != nil {
		return nil, ErrState
	}
	info, e := os.Lstat(cfg.StateDir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrState
	}
	lockPath := filepath.Join(cfg.StateDir, "budget.lock")
	_, lockStatErr := os.Lstat(lockPath)
	lockExisted := lockStatErr == nil
	if st, e := os.Lstat(lockPath); e == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return nil, ErrState
	}
	lock, e := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, ErrState
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, ErrState
	}
	release := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	zone, e := time.LoadLocation("Asia/Seoul")
	if e != nil {
		release()
		return nil, ErrState
	}
	ctx, cancel := context.WithCancel(context.Background())
	stateProvider := provider
	if provider == "odsay" {
		stateProvider = ""
	}
	live := &Live{cfg: cfg, provider: provider, upstream: upstream, lock: lock, now: time.Now, zone: zone, cache: map[string]cachedRoute{}, flights: map[string]*routeFlight{}, ctx: ctx, cancel: cancel, state: budgetState{Version: 1, Provider: stateProvider}}
	path := filepath.Join(cfg.StateDir, "budget.json")
	if st, e := os.Lstat(path); e == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		release()
		cancel()
		return nil, ErrState
	}
	raw, e := os.ReadFile(path)
	if e == nil {
		if len(raw) > 4096 || json.Unmarshal(raw, &live.state) != nil || (live.state.Provider != provider && !(provider == "odsay" && live.state.Provider == "")) || live.state.Version != 1 || live.state.DailyUsed < 0 || live.state.TotalUsed < 0 || live.state.DailyUsed > live.state.TotalUsed {
			release()
			cancel()
			return nil, ErrState
		}
		if day, e := time.Parse("2006-01-02", live.state.Day); e != nil || day.Format("2006-01-02") != live.state.Day {
			release()
			cancel()
			return nil, ErrState
		}
	} else if !os.IsNotExist(e) {
		release()
		cancel()
		return nil, ErrState
	}
	if os.IsNotExist(e) {
		// A previous lock without its ledger may mean accidental deletion: never silently reset.
		if lockExisted {
			release()
			cancel()
			return nil, ErrState
		}
		live.state.Day = live.now().In(zone).Format("2006-01-02")
		if live.saveLocked() != nil {
			release()
			cancel()
			return nil, ErrState
		}
	}
	return live, nil
}

func (l *Live) saveLocked() error {
	b, e := json.MarshalIndent(l.state, "", "  ")
	if e != nil {
		return ErrState
	}
	f, e := os.CreateTemp(l.cfg.StateDir, ".budget-*")
	if e != nil {
		return ErrState
	}
	path := f.Name()
	defer os.Remove(path)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if ce := f.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return ErrState
	}
	if os.Rename(path, filepath.Join(l.cfg.StateDir, "budget.json")) != nil {
		return ErrState
	}
	dir, e := os.Open(l.cfg.StateDir)
	if e != nil {
		return ErrState
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrState
	}
	return nil
}
func (l *Live) reserveLocked() error {
	if l.cfg.Reserve != nil {
		return l.cfg.Reserve(l.ctx)
	}
	day := l.now().In(l.zone).Format("2006-01-02")
	// A backward clock jump must not reset the daily budget.
	if l.state.Day > day {
		return ErrState
	}
	if l.state.Day != day {
		l.state.Day = day
		l.state.DailyUsed = 0
	}
	if l.state.DailyUsed+l.cfg.ExternalReserve >= l.cfg.DailyLimit || l.state.TotalUsed >= l.cfg.TotalLimit {
		return ErrBudget
	}
	l.state.DailyUsed++
	l.state.TotalUsed++
	if e := l.saveLocked(); e != nil {
		l.poisoned = true
		return e
	}
	return nil
}
func routeCacheKey(q Query) string {
	val := func(f float64) string {
		if f == 0 {
			return "0"
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	kind := "urban-all-v1"
	if q.Mode == "walk" {
		kind = "walking-v1"
	}
	return val(q.Start.Lon) + "," + val(q.Start.Lat) + ">" + val(q.End.Lon) + "," + val(q.End.Lat) + "|" + kind
}

func (l *Live) Search(ctx context.Context, q Query) (Response, string, error) {
	if l.provider == "odsay" && q.Mode == "walk" {
		return Response{}, "odsay_live", ErrUnsupportedMode
	}
	if !q.Valid() {
		return Response{}, l.provider + "_live", ErrInput
	}
	if ctx.Err() != nil {
		return Response{}, l.provider + "_live", ctx.Err()
	}
	key := routeCacheKey(q)
	l.mu.Lock()
	if l.closed || l.poisoned {
		l.mu.Unlock()
		return Response{}, l.provider + "_live", ErrState
	}
	now := l.now()
	for k, v := range l.cache {
		if !now.Before(v.expires) {
			delete(l.cache, k)
		}
	}
	if entry, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return entry.response, l.provider + "_live_cached", entry.err
	}
	flight, exists := l.flights[key]
	if !exists {
		if len(l.flights) >= 16 {
			l.mu.Unlock()
			return Response{}, l.provider + "_live", ErrBudget
		}
		if e := l.reserveLocked(); e != nil {
			l.mu.Unlock()
			return Response{}, l.provider + "_live", e
		}
		flight = &routeFlight{done: make(chan struct{})}
		l.flights[key] = flight
		if q.Mode != "walk" {
			q.Mode = "all"
		} // cache transit independently of walking
		l.wg.Add(1)
		go l.fetch(key, q, flight)
	}
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return Response{}, l.provider + "_live", ctx.Err()
	case <-flight.done:
		return flight.result.response, l.provider + "_live", flight.result.err
	}
}
func (l *Live) fetch(key string, q Query, flight *routeFlight) {
	defer l.wg.Done()
	// A caller disconnect cannot trigger a second paid request for the same coordinate pair.
	ctx, cancel := context.WithTimeout(l.ctx, 8*time.Second)
	defer cancel()
	response, _, e := l.upstream.Search(ctx, q)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		e = ErrTimeout
	}
	ttl := l.cfg.CacheTTL
	if e != nil {
		ttl = time.Minute
		response = Response{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := cachedRoute{received: l.now(), response: response, err: e, expires: l.now().Add(ttl)}
	if len(l.cache) >= 128 {
		for k := range l.cache {
			delete(l.cache, k)
			break
		}
	}
	l.cache[key] = entry
	flight.result = entry
	delete(l.flights, key)
	close(flight.done)
}
func (l *Live) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.wg.Wait()
	if l.lock == nil {
		return nil
	}
	syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
	return l.lock.Close()
}

// ReceivedAt preserves provider response age when several searches reuse a cache entry.
func (l *Live) ReceivedAt(q Query) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cache[routeCacheKey(q)].received
}
