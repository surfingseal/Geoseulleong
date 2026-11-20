package gbis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type ValidationBudgetConfig struct {
	Directory                                    string
	ReserveCall                                  func(context.Context) error
	DailyLimit, TotalLimit, ExternalDailyReserve int
}
type validationBudgetState struct {
	Version    int       `json:"version"`
	Provider   string    `json:"provider"`
	KeyTag     string    `json:"key_fingerprint"`
	Day        string    `json:"day_asia_seoul"`
	Daily      int       `json:"daily_reserved"`
	Total      int       `json:"total_reserved"`
	ReservedAt time.Time `json:"last_reserved_at"`
	ClosedAt   time.Time `json:"last_closed_at"`
}
type ValidationBudgetStatus struct {
	StateUsable     bool   `json:"state_usable"`
	Day             string `json:"day_asia_seoul"`
	DailyReserved   int    `json:"daily_reserved"`
	TotalReserved   int    `json:"total_reserved"`
	DailyLimit      int    `json:"daily_limit"`
	TotalLimit      int    `json:"total_limit"`
	ExternalReserve int    `json:"external_daily_reserve"`
}
type ValidationBudget struct {
	mu                     sync.Mutex
	cfg                    ValidationBudgetConfig
	state                  validationBudgetState
	lock                   *os.File
	now                    func() time.Time
	zone                   *time.Location
	next                   time.Time
	interval               time.Duration
	closed, poisoned, used bool
}

func budgetError(kind string) error { return &Error{Kind: kind} }

// NewPersistentValidationClient is the CLI's only live path. An approval failure
// happens before opening files. Every permitted request reserves before Do.
func NewPersistentValidationClient(key string, approved bool, maxArrivals int, cfg ValidationBudgetConfig, sink func(ValidationCapture) error) (*Client, *ValidationBudget, error) {
	c, e := NewValidationClient(key, approved, maxArrivals, sink)
	if e != nil {
		return nil, nil, e
	}
	b, e := openValidationBudget(c.key, cfg)
	if e != nil {
		return nil, nil, e
	}
	if e = b.CheckCapacity(1 + maxArrivals); e != nil {
		b.Close()
		return nil, nil, e
	}
	c.validation.before = b.Reserve
	return c, b, nil
}
func privateFile(path string, missing bool) bool {
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return missing
	}
	return e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0077 == 0
}
func openValidationBudget(key string, cfg ValidationBudgetConfig) (*ValidationBudget, error) {
	if (cfg.ReserveCall == nil && !filepath.IsAbs(cfg.Directory)) || cfg.DailyLimit < 1 || cfg.DailyLimit > 1000000 || cfg.TotalLimit < 1 || cfg.TotalLimit > 1000000 || cfg.ExternalDailyReserve < 0 || cfg.ExternalDailyReserve >= cfg.DailyLimit {
		return nil, budgetError("invalid_persistent_budget")
	}
	if cfg.ReserveCall != nil {
		return &ValidationBudget{cfg: cfg}, nil
	}
	if e := os.MkdirAll(cfg.Directory, 0700); e != nil {
		return nil, budgetError("budget_state_unavailable")
	}
	st, e := os.Lstat(cfg.Directory)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, budgetError("budget_directory_not_private")
	}
	lockPath := filepath.Join(cfg.Directory, "budget.lock")
	_, err := os.Lstat(lockPath)
	lockExisted := err == nil
	if !privateFile(lockPath, true) {
		return nil, budgetError("budget_lock_invalid")
	}
	fd, e := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, budgetError("budget_state_unavailable")
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	release := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, budgetError("budget_already_in_use")
	}
	zone, e := time.LoadLocation("Asia/Seoul")
	if e != nil {
		release()
		return nil, budgetError("budget_state_unavailable")
	}
	h := sha256.Sum256([]byte(key))
	tag := hex.EncodeToString(h[:])
	b := &ValidationBudget{cfg: cfg, lock: lock, now: time.Now, zone: zone, interval: 2 * time.Second, state: validationBudgetState{Version: 1, Provider: "gbis_validation", KeyTag: tag}}
	path := filepath.Join(cfg.Directory, "budget.json")
	if !privateFile(path, true) {
		release()
		return nil, budgetError("budget_state_invalid")
	}
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		if lockExisted {
			release()
			return nil, budgetError("budget_state_missing_no_reset")
		}
		if e = b.save(); e != nil {
			release()
			return nil, e
		}
	} else if e != nil {
		release()
		return nil, budgetError("budget_state_unavailable")
	} else {
		raw, ee := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if ee != nil || len(raw) > 4096 || json.Unmarshal(raw, &b.state) != nil || b.state.Version != 1 || b.state.Provider != "gbis_validation" || b.state.KeyTag != tag || b.state.Total < 0 || b.state.Daily < 0 || b.state.Daily > b.state.Total || b.state.Total > 0 && (b.state.ReservedAt.IsZero() || b.state.Day == "") {
			release()
			return nil, budgetError("budget_state_invalid")
		}
	}
	if b.state.Day != "" {
		day, e := time.Parse("2006-01-02", b.state.Day)
		if e != nil || day.Format("2006-01-02") != b.state.Day {
			release()
			return nil, budgetError("budget_state_invalid")
		}
	}
	if b.state.ReservedAt.After(b.now().Add(5*time.Second)) || b.state.ClosedAt.After(b.now().Add(5*time.Second)) {
		release()
		return nil, budgetError("budget_clock_reversed")
	}
	// A clean prior run finished all requests before releasing its lock. Following
	// an unclean exit, wait from this lock acquisition instead; never refund calls.
	if b.state.Total > 0 {
		if !b.state.ClosedAt.IsZero() && !b.state.ClosedAt.Before(b.state.ReservedAt) {
			b.next = b.state.ClosedAt.Add(b.interval)
		} else {
			b.next = b.now().Add(b.interval)
		}
	}
	return b, nil
}
func (b *ValidationBudget) save() error {
	path := filepath.Join(b.cfg.Directory, "budget.json")
	if !privateFile(path, true) {
		return budgetError("budget_state_invalid")
	}
	raw, e := json.Marshal(b.state)
	if e != nil {
		return budgetError("budget_state_unavailable")
	}
	f, e := os.CreateTemp(b.cfg.Directory, ".gbis-reservation-*")
	if e != nil {
		return budgetError("budget_state_unavailable")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return budgetError("budget_state_unavailable")
	}
	if e = os.Rename(name, path); e != nil {
		return budgetError("budget_state_unavailable")
	}
	dir, e := os.Open(b.cfg.Directory)
	if e != nil {
		return budgetError("budget_state_unavailable")
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return budgetError("budget_state_unavailable")
	}
	return nil
}
func (b *ValidationBudget) counts() (string, int) {
	day := b.now().In(b.zone).Format("2006-01-02")
	daily := b.state.Daily
	if b.state.Day != "" && day < b.state.Day {
		return "", daily
	}
	if day != b.state.Day {
		daily = 0
	}
	return day, daily
}
func (b *ValidationBudget) CheckCapacity(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.check(n)
}
func (b *ValidationBudget) check(n int) error {
	if b.cfg.ReserveCall != nil && !b.closed {
		return nil
	}
	if b.closed || b.poisoned || n < 1 {
		return budgetError("budget_state_unavailable")
	}
	day, daily := b.counts()
	if day == "" {
		return budgetError("budget_clock_reversed")
	}
	if n > b.cfg.TotalLimit-b.state.Total || n > b.cfg.DailyLimit-b.cfg.ExternalDailyReserve-daily {
		return budgetError("persistent_budget_exhausted")
	}
	return nil
}
func (b *ValidationBudget) Reserve(ctx context.Context) error {
	if b.cfg.ReserveCall != nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.closed {
			return budgetError("budget_state_unavailable")
		}
		return b.cfg.ReserveCall(ctx)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.check(1); e != nil {
		return e
	}
	if delay := b.next.Sub(b.now()); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return budgetError("cancelled")
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return budgetError("cancelled")
	}
	if e := b.check(1); e != nil {
		return e
	}
	day, daily := b.counts()
	b.state.Day = day
	b.state.Daily = daily + 1
	b.state.Total++
	b.state.ReservedAt = b.now()
	b.next = b.state.ReservedAt.Add(b.interval)
	if e := b.save(); e != nil {
		b.poisoned = true
		return e
	}
	b.used = true
	return nil
}
func (b *ValidationBudget) Status() ValidationBudgetStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return ValidationBudgetStatus{StateUsable: !b.poisoned, Day: b.state.Day, DailyReserved: b.state.Daily, TotalReserved: b.state.Total, DailyLimit: b.cfg.DailyLimit, TotalLimit: b.cfg.TotalLimit, ExternalReserve: b.cfg.ExternalDailyReserve}
}
func (b *ValidationBudget) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	var e error
	if b.used && !b.poisoned {
		b.state.ClosedAt = b.now()
		e = b.save()
		if e != nil {
			b.poisoned = true
		}
	}
	b.closed = true
	if b.lock == nil {
		return e
	}
	syscall.Flock(int(b.lock.Fd()), syscall.LOCK_UN)
	ce := b.lock.Close()
	if e != nil {
		return e
	}
	return ce
}

// NewPersistentRealtimeClient reuses the transport, credential filtering and
// durable daily/total reservations. The HTTP service caps each query at 1+4;
// its shared 30s cache deduplicates requests across selected paths. Unlike the
// one-shot diagnostic CLI, this client may refresh after TTL while budget remains.
func NewPersistentRealtimeClient(key string, approved bool, cfg ValidationBudgetConfig) (*Client, *ValidationBudget, error) {
	c, e := NewValidationClient(key, approved, 4, nil)
	if e != nil {
		return nil, nil, e
	}
	b, e := openValidationBudget(c.key, cfg)
	if e != nil {
		return nil, nil, e
	}
	if e = b.CheckCapacity(1); e != nil {
		b.Close()
		return nil, nil, e
	}
	c.maxCalls = cfg.TotalLimit
	c.validation.realtime = true
	c.validation.before = b.Reserve
	return c, b, nil
}
