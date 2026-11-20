package collectionrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/collectionpreview"
	cp "geoseulleong/internal/congestionpipeline"
	rc "geoseulleong/internal/routecongestion"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Config struct {
	FixtureOnly bool                       `json:"fixture_only"`
	Manifest    collectionpreview.Manifest `json:"manifest"`
	Identities  []Identity                 `json:"identities"`
	DailyLimit  int                        `json:"daily_request_limit"`
	TotalLimit  int                        `json:"total_request_limit"`
	MaxBytes    int64                      `json:"max_response_payload_bytes"`
}
type Permit struct {
	scopes     map[rc.Scope]bool
	identities map[string]Identity
	Config     Config
	Hash       string
	Preview    collectionpreview.Preview
}

func Prepare(c Config) (*Permit, error) {
	p, e := collectionpreview.Build(c.Manifest, nil)
	if e != nil {
		return nil, e
	}
	if c.DailyLimit < 1 || c.TotalLimit < 1 || c.TotalLimit > 1000000 || c.MaxBytes < 1 || c.TotalLimit < int(c.Manifest.Budget.MaxRequests) {
		return nil, errors.New("explicit persistent budgets missing")
	}
	if c.Manifest.Origin != "existing_and_proposed" && !(c.FixtureOnly && c.Manifest.Origin == "synthetic_test") {
		return nil, errors.New("synthetic manifests cannot authorize collection")
	}
	permit := &Permit{scopes: map[rc.Scope]bool{}, identities: map[string]Identity{}, Config: c, Preview: p}
	b, _ := json.Marshal(c)
	permit.Hash = Digest(b)
	for _, id := range c.Identities {
		if _, ok := permit.identities[id.STCIS]; ok {
			return nil, errors.New("duplicate route identity")
		}
		permit.identities[id.STCIS] = id
	}
	for _, r := range c.Manifest.Routes {
		id, ok := permit.identities[r.STCIS]
		if !ok {
			return nil, errors.New("official identity missing")
		}
		if e = id.Verify(r); e != nil {
			return nil, e
		}
		b, _ := json.Marshal(id)
		if Digest(b) != r.EvidenceSHA {
			return nil, errors.New("identity evidence hash mismatch")
		}
	}
	for _, t := range p.Tasks {
		for _, b := range t.Blockers {
			if !(c.FixtureOnly && b == "synthetic_fixture_not_official_evidence") {
				return nil, errors.New("preview blocked")
			}
		}
		permit.scopes[t.Scope] = true
	}
	return permit, nil
}
func (p *Permit) Allows(s rc.Scope) bool { return p != nil && p.scopes[s] }

type Ledger struct {
	Version    int            `json:"version"`
	Total      int            `json:"total_reserved_requests"`
	Daily      map[string]int `json:"daily_reserved_requests_kst"`
	TotalLimit int            `json:"total_limit"`
	DailyLimit int            `json:"daily_limit"`
	Bytes      int64          `json:"response_bytes"`
	MaxBytes   int64          `json:"max_bytes"`
	Last       time.Time      `json:"last_request_start"`
}
type Checkpoint struct {
	Transfer *rc.TransferReceipt `json:"transfer_receipt,omitempty"`
	Origin   string              `json:"data_origin"`
	Scope    rc.Scope            `json:"scope"`
	Page     int                 `json:"page"`
	Status   string              `json:"status"`
	Raw      []byte              `json:"raw_response_base64"`
	HTTP     int                 `json:"http_status"`
	SHA      string              `json:"sha256"`
	At       time.Time           `json:"responded_at"`
	Plan     string              `json:"authorization_hash"`
}
type Result struct {
	Scope   rc.Scope    `json:"scope"`
	Status  string      `json:"status"`
	ID      string      `json:"snapshot_id"`
	Batch   *cp.Batch   `json:"batch,omitempty"`
	Outcome *Checkpoint `json:"outcome,omitempty"`
}
type RunResult struct {
	Incomplete    bool       `json:"incomplete,omitempty"`
	Failed        []rc.Scope `json:"failed_scopes,omitempty"`
	Origin        string     `json:"data_origin"`
	Authorization string     `json:"authorization_hash"`
	Results       []Result   `json:"results"`
	Calls         int        `json:"new_transport_requests"`
}
type Runner struct {
	SkipFailed bool // explicit operator choice: never retry failed/pending scopes
	Permit     *Permit
	Fetcher    cp.Fetcher
	Directory  string
	LedgerPath string
}

func lock(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("global collector already running")
	}
	return f, nil
}
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
func read(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if json.Unmarshal(b, v) != nil {
		return errors.New("checkpoint/ledger corrupt")
	}
	return nil
}
func wait(ctx context.Context, until time.Time) error {
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run holds one process-wide file lock across all scopes. All users of the API key
// must share this absolute ledger path; distributed deployment needs a shared ledger.
func (r Runner) Run(ctx context.Context) (out RunResult, err error) {
	if r.Permit == nil || r.Fetcher == nil || !filepath.IsAbs(r.LedgerPath) || !filepath.IsAbs(r.Directory) {
		return out, errors.New("absolute state/ledger paths and verified permit required")
	}
	// Revalidate mutable input; a Permit is not an authorization to alter its manifest.
	checked, e := Prepare(r.Permit.Config)
	if e != nil || checked.Hash != r.Permit.Hash {
		return out, errors.New("permit mutated")
	}
	p := checked
	if e = os.MkdirAll(filepath.Dir(r.LedgerPath), 0700); e != nil {
		return out, e
	}
	l, e := lock(r.LedgerPath + ".lock")
	if e != nil {
		return out, e
	}
	defer unlock(l)
	ledger := Ledger{Version: 1, Daily: map[string]int{}, TotalLimit: p.Config.TotalLimit, DailyLimit: p.Config.DailyLimit, MaxBytes: p.Config.MaxBytes}
	if e = read(r.LedgerPath, &ledger); e != nil && !os.IsNotExist(e) {
		return out, e
	}
	if ledger.Version != 1 || ledger.Daily == nil || ledger.Total < 0 || ledger.Bytes < 0 || ledger.TotalLimit != p.Config.TotalLimit || ledger.DailyLimit != p.Config.DailyLimit || ledger.MaxBytes != p.Config.MaxBytes {
		return out, errors.New("persistent ledger policy mismatch/corruption")
	}
	sum := 0
	for day, n := range ledger.Daily {
		if _, e := time.Parse("2006-01-02", day); e != nil || n < 0 || n > ledger.DailyLimit {
			return out, errors.New("invalid daily ledger")
		}
		sum += n
	}
	if sum != ledger.Total || ledger.Total > ledger.TotalLimit || ledger.Bytes > ledger.MaxBytes {
		return out, errors.New("inconsistent persistent ledger")
	}
	if e = os.MkdirAll(r.Directory, 0700); e != nil {
		return out, e
	}
	stateLock, e := lock(filepath.Join(r.Directory, ".lock"))
	if e != nil {
		return out, e
	}
	defer unlock(stateLock)
	// Bind one checkpoint root to an immutable approved manifest.
	binding := filepath.Join(r.Directory, "authorization.json")
	var h string
	if e = read(binding, &h); e == nil {
		if h != p.Hash {
			return out, errors.New("checkpoint root belongs to different plan")
		}
	} else if os.IsNotExist(e) {
		if e = persist(binding, p.Hash); e != nil {
			return out, e
		}
	} else {
		return out, e
	}
	if e = persist(filepath.Join(r.Directory, "execution-config.json"), p.Config); e != nil {
		return out, e
	}
	out.Authorization = p.Hash
	out.Origin = "injected_test_transport"
	switch r.Fetcher.(type) {
	case Files:
		out.Origin = "recorded_response_files"
	case *rc.Client:
		out.Origin = r.Fetcher.(*rc.Client).DataOrigin()
	}
tasks:
	for _, task := range p.Preview.Tasks {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		s := task.Scope
		dir := filepath.Join(r.Directory, Digest([]byte(collectionpreview.ScopeKey(s))))
		if e = os.MkdirAll(dir, 0700); e != nil {
			return out, e
		}
		pages := []cp.Page{}
		needed := 1
		var total *int
		var result Result
		for n := 1; n <= needed; n++ {
			path := filepath.Join(dir, fmt.Sprintf("page-%04d.checkpoint.json", n))
			var c Checkpoint
			e = read(path, &c)
			if e == nil {
				if c.Scope != s || c.Page != n || c.Plan != p.Hash {
					return out, errors.New("checkpoint provenance/hash mismatch")
				}
				if c.Status != "success" && c.Status != "no_data_for_query" {
					if r.SkipFailed && (c.Status == "failed" || c.Status == "ambiguous" || c.Status == "pending") {
						out.Incomplete = true
						out.Failed = append(out.Failed, s)
						continue tasks
					}
					return out, errors.New("failed/uncertain request requires manual review; never automatically retried")
				}
				if c.SHA != Digest(c.Raw) || c.At.IsZero() {
					return out, errors.New("checkpoint hash/time mismatch")
				}
			} else if !os.IsNotExist(e) {
				return out, e
			} else {
				if n > int(p.Config.Manifest.Budget.MaxPages) {
					return out, errors.New("scope page budget reached")
				}
				day := time.Now().In(time.FixedZone("Asia/Seoul", 9*3600)).Format("2006-01-02")
				if ledger.Total >= ledger.TotalLimit || ledger.Daily[day] >= ledger.DailyLimit || out.Calls >= int(p.Config.Manifest.Budget.MaxRequests) || ledger.Bytes > ledger.MaxBytes-rc.MaxReadBytes {
					return out, errors.New("persistent/run request or archive budget reached")
				}
				if e = wait(ctx, ledger.Last.Add(time.Duration(p.Config.Manifest.Budget.Interval)*time.Second)); e != nil {
					return out, e
				}
				day = time.Now().In(time.FixedZone("Asia/Seoul", 9*3600)).Format("2006-01-02")
				if ledger.Daily[day] >= ledger.DailyLimit {
					return out, errors.New("daily budget reached")
				}
				ledger.Total++
				ledger.Daily[day]++
				ledger.Bytes += rc.MaxReadBytes
				ledger.Last = time.Now().UTC()
				if e = persist(r.LedgerPath, ledger); e != nil {
					return out, e
				}
				c = Checkpoint{Scope: s, Page: n, Status: "pending", Plan: p.Hash, Origin: out.Origin}
				if e = persist(path, c); e != nil {
					return out, e
				}
				bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
				var raw []byte
				var status int
				var fetchErr error
				var transfer rc.TransferReceipt
				if measured, ok := r.Fetcher.(interface {
					GetMeasured(context.Context, rc.Scope, int) ([]byte, int, rc.TransferReceipt, error)
				}); ok {
					raw, status, transfer, fetchErr = measured.GetMeasured(bounded, s, n)
				} else {
					raw, status, fetchErr = r.Fetcher.Get(bounded, s, n)
					// Legacy/file fetchers cannot prove completeness on errors. Never refund
					// a reservation based solely on an empty raw slice in an error path.
					transfer = rc.TransferReceipt{BodyBytes: int64(len(raw)), BytesKnown: fetchErr == nil, BodyComplete: fetchErr == nil}
				}
				c.Transfer = &transfer
				cancel()
				out.Calls++
				c.Raw = raw
				c.HTTP = status
				c.SHA = Digest(raw)
				c.At = time.Now().UTC()
				c.Status = "failed"
				if len(raw) > rc.MaxBodyBytes {
					fetchErr = errors.New("response size limit")
				}
				if transfer.BodyBytes < 0 || transfer.BodyBytes > rc.MaxReadBytes || transfer.WriteAttempts > 1 || transfer.ConnectionReused {
					fetchErr = errors.New("transport accounting invariant failed")
				}
				parsed, parseErr := rc.Parse(raw)
				if fetchErr != nil {
					if status == 0 {
						c.Status = "ambiguous"
					}
				} else if status == 200 && errors.Is(parseErr, rc.ErrNoData) && parsed.Code == "50" && n == 1 {
					c.Status = "no_data_for_query"
				} else if _, ce := cp.CheckAuthorized(raw, status, s, n, p.Allows); ce == nil {
					c.Status = "success"
				}
				// Response is saved even when invalid; no error strings/URLs/keys are logged.
				if e = persist(path, c); e != nil {
					return out, e
				}
				rawPath := filepath.Join(dir, fmt.Sprintf("page-%04d.raw.json", n))
				if e = writeOnce(rawPath, raw); e != nil {
					return out, e
				}
				ledger.Last = c.At
				// Only a completely measured body can release the unused reservation.
				// Partial reads, unknown transport failures and oversize probes retain it.
				charged := int64(rc.MaxReadBytes)
				if transfer.BytesKnown && transfer.BodyComplete && transfer.BodyBytes >= int64(len(raw)) && transfer.BodyBytes >= 0 && transfer.BodyBytes <= rc.MaxReadBytes {
					charged = transfer.BodyBytes
				}
				if transfer.BodyBytes > charged {
					charged = transfer.BodyBytes
				}
				ledger.Bytes += charged - rc.MaxReadBytes
				if e = persist(r.LedgerPath, ledger); e != nil {
					return out, e
				}
				if c.Status == "failed" || c.Status == "ambiguous" {
					if r.SkipFailed {
						out.Incomplete = true
						out.Failed = append(out.Failed, s)
						continue tasks
					}
					return out, errors.New("upstream/validation failure; manual review required")
				}
			}
			// A crash after checkpoint persistence can be repaired without a new request.
			if e = writeOnce(filepath.Join(dir, fmt.Sprintf("page-%04d.raw.json", n)), c.Raw); e != nil {
				return out, e
			}
			if c.Status == "no_data_for_query" {
				parsed, pe := rc.Parse(c.Raw)
				if c.HTTP != 200 || !errors.Is(pe, rc.ErrNoData) || parsed.Code != "50" || n != 1 {
					return out, errors.New("invalid code50 checkpoint")
				}
				b, _ := json.Marshal(s)
				result = Result{Scope: s, Status: c.Status, ID: Digest(append(b, []byte(c.SHA)...)), Outcome: &c}
				break
			}
			parsed, e := cp.CheckAuthorized(c.Raw, c.HTTP, s, n, p.Allows)
			if e != nil {
				return out, e
			}
			if total == nil {
				v := parsed.Total
				total = &v
				needed = max(1, (v+999)/1000)
			} else if *total != parsed.Total {
				return out, errors.New("totalCount changed")
			}
			pages = append(pages, cp.Page{Number: n, HTTP: c.HTTP, Raw: c.Raw, SHA: c.SHA, At: c.At})
			if needed > int(p.Config.Manifest.Budget.MaxPages) {
				return out, errors.New("discovered total exceeds approved page cap; first page preserved")
			}
		}
		if result.Status == "" {
			b, e := cp.BuildAuthorized(s, pages, p.Allows)
			if e != nil {
				return out, e
			}
			result = Result{Scope: s, Status: "complete", ID: b.ID, Batch: &b}
		}
		if e = persist(filepath.Join(dir, "result.json"), result); e != nil {
			return out, e
		}
		out.Results = append(out.Results, result)
	}
	name := "completed.json"
	if out.Incomplete {
		name = "partial.json"
	}
	err = persist(filepath.Join(r.Directory, name), out)
	return out, err
}
func writeOnce(path string, b []byte) error {
	if old, e := os.ReadFile(path); e == nil {
		if string(old) != string(b) {
			return errors.New("immutable raw response conflict")
		}
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	return f.Sync()
}

func persist(path string, v any) error {
	if e := cp.Atomic(path, v); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
