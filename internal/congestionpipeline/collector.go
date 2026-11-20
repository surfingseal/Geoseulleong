package congestionpipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/routecongestion"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Fetcher interface {
	Get(context.Context, routecongestion.Scope, int) ([]byte, int, error)
}
type State struct {
	Scope   routecongestion.Scope `json:"scope"`
	Number  int                   `json:"page"`
	Attempt int                   `json:"attempt"`
	Status  string                `json:"status"`
	HTTP    int                   `json:"http_status"`
	Raw     []byte                `json:"raw_response_base64"`
	At      time.Time             `json:"at"`
	Error   string                `json:"error,omitempty"`
}

func Atomic(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}

// Collect resumes successful pages. Pending/ambiguous requests never auto-repeat.
func Collect(ctx context.Context, f Fetcher, scope routecongestion.Scope, dir string, budget int, retryFailed bool) (Batch, int, error) {
	if !scope.Allowed() || budget < 0 || budget > 1000 {
		return Batch{}, 0, errors.New("invalid approved scope or request budget")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return Batch{}, 0, e
	}
	lock, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return Batch{}, 0, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return Batch{}, 0, errors.New("collector already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	pages := []Page{}
	calls := 0
	needed := 1
	for number := 1; number <= needed; number++ {
		path := filepath.Join(dir, fmt.Sprintf("page-%04d.state.json", number))
		var state State
		if data, e := os.ReadFile(path); e == nil {
			if json.Unmarshal(data, &state) != nil || state.Scope != scope || state.Number != number {
				return Batch{}, calls, errors.New("checkpoint scope mismatch")
			}
		} else if !os.IsNotExist(e) {
			return Batch{}, calls, e
		}
		if state.Status != "success" {
			if state.Status == "pending" || state.Status == "ambiguous" {
				return Batch{}, calls, errors.New("request transmission uncertain; manual inspection required")
			}
			if state.Status == "failed" && !retryFailed {
				return Batch{}, calls, errors.New("known failed checkpoint; explicit retry-failed required")
			}
			if calls >= budget {
				return Batch{}, calls, errors.New("request budget reached")
			}
			state = State{Scope: scope, Number: number, Attempt: state.Attempt + 1, Status: "pending", At: time.Now().UTC()}
			if e = Atomic(path, state); e != nil {
				return Batch{}, calls, e
			}
			raw, status, fetchErr := f.Get(ctx, scope, number)
			calls++
			state.Raw = raw
			state.HTTP = status
			if fetchErr != nil {
				state.Error = fetchErr.Error()
				state.Status = "failed"
				if status == 0 {
					state.Status = "ambiguous"
				}
			} else if _, e := Check(raw, status, scope, number); e != nil {
				state.Status = "failed"
				state.Error = e.Error()
			} else {
				state.Status = "success"
			}
			if e = Atomic(path, state); e != nil {
				return Batch{}, calls, e
			}
			if e = Atomic(filepath.Join(dir, fmt.Sprintf("page-%04d-attempt-%02d.json", number, state.Attempt)), state); e != nil {
				return Batch{}, calls, e
			}
			if state.Status != "success" {
				return Batch{}, calls, errors.New(state.Error)
			}
		}
		p, e := Check(state.Raw, state.HTTP, scope, number)
		if e != nil {
			return Batch{}, calls, e
		}
		if number == 1 {
			needed = max(1, (p.Total+999)/1000)
		}
		rawPath := filepath.Join(dir, fmt.Sprintf("page-%04d.json", number))
		if existing, e := os.ReadFile(rawPath); e == nil {
			if string(existing) != string(state.Raw) {
				return Batch{}, calls, errors.New("raw file differs from checkpoint")
			}
		} else if os.IsNotExist(e) {
			if e = os.WriteFile(rawPath, state.Raw, 0600); e != nil {
				return Batch{}, calls, e
			}
		} else {
			return Batch{}, calls, e
		}
		pages = append(pages, Page{Number: number, HTTP: state.HTTP, Raw: state.Raw, At: state.At})
	}
	b, e := Build(scope, pages)
	if e == nil {
		e = Atomic(filepath.Join(dir, "batch.json"), b)
	}
	return b, calls, e
}

// Seed imports already fetched raw pages without making HTTP calls.
func Seed(dir string, b Batch) error {
	checked, e := Build(b.Scope, b.Pages)
	if e != nil {
		return e
	}
	b = checked
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("collector already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	for _, p := range b.Pages {
		path := filepath.Join(dir, fmt.Sprintf("page-%04d.state.json", p.Number))
		if data, e := os.ReadFile(path); e == nil {
			var state State
			if json.Unmarshal(data, &state) != nil || state.Scope != b.Scope || state.Status != "success" || string(state.Raw) != string(p.Raw) {
				return errors.New("seed checkpoint conflict")
			}
		} else if os.IsNotExist(e) {
			if e = Atomic(path, State{Scope: b.Scope, Number: p.Number, Status: "success", HTTP: p.HTTP, Raw: p.Raw, At: p.At}); e != nil {
				return e
			}
		} else {
			return e
		}
		rawPath := filepath.Join(dir, fmt.Sprintf("page-%04d.json", p.Number))
		if data, e := os.ReadFile(rawPath); e == nil {
			if string(data) != string(p.Raw) {
				return errors.New("seed raw page conflict")
			}
		} else if os.IsNotExist(e) {
			if e = os.WriteFile(rawPath, p.Raw, 0600); e != nil {
				return e
			}
		} else {
			return e
		}
	}
	return Atomic(filepath.Join(dir, "batch.json"), b)
}
