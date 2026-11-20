package alternatives

import (
	"encoding/json"
	"errors"
	cr "geoseulleong/internal/collectionrunner"
	cp "geoseulleong/internal/congestionpipeline"
	rc "geoseulleong/internal/routecongestion"
	"io"
	"os"
	"strconv"
	"time"
)

// LoadFiles reads completed results only. No runner, HTTP client or ledger is opened.
func LoadFiles(path string) ([]Observation, int, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, 0, errors.New("additional observations file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 32<<20+1))
	if e != nil || len(b) > 32<<20 {
		return nil, 0, errors.New("additional observations oversized")
	}
	var run cr.RunResult
	if json.Unmarshal(b, &run) != nil || run.Incomplete {
		return nil, 0, errors.New("incomplete additional observations")
	}
	out := []Observation{}
	code50 := 0
	seen := map[rc.Scope]bool{}
	allowed := func(s rc.Scope) bool {
		return s.Route == "41002905" && (s.Date == "20261002" || s.Date == "20261003" || s.Date == "20261005")
	}
	for _, r := range run.Results {
		if !allowed(r.Scope) || seen[r.Scope] {
			return nil, 0, errors.New("unexpected or duplicate scope")
		}
		seen[r.Scope] = true
		if r.Status == "no_data_for_query" {
			if r.Outcome == nil {
				return nil, 0, errors.New("missing outcome")
			}
			p, e := rc.Parse(r.Outcome.Raw)
			if !errors.Is(e, rc.ErrNoData) || p.Code != "50" {
				return nil, 0, errors.New("invalid code50")
			}
			code50++
			continue
		}
		if r.Status != "complete" || r.Batch == nil {
			return nil, 0, errors.New("unsuccessful source scope")
		}
		batch, e := cp.BuildAuthorized(r.Scope, r.Batch.Pages, allowed)
		if e != nil || batch.ID != r.ID {
			return nil, 0, errors.New("source batch invalid")
		}
		d, e := time.Parse("20060102", r.Scope.Date)
		if e != nil {
			return nil, 0, e
		}
		for _, p := range batch.Pages {
			for _, raw := range p.Rows {
				seq, e := strconv.Atoi(cp.Value(raw, "sttn_seq"))
				if e != nil {
					return nil, 0, e
				}
				row := Observation{Date: d.Format("2006-01-02"), Stop: cp.Value(raw, "sttn_id"), Sequence: &seq, TZON: cp.Value(raw, "tzon")}
				v := cp.Value(raw, "cgst")
				if v != "" && v != "null" {
					x, e := strconv.ParseFloat(v, 64)
					if e != nil {
						return nil, 0, errors.New("invalid cgst")
					}
					row.CGST = &x
				}
				out = append(out, row)
			}
		}
	}
	if len(seen) == 0 {
		return nil, 0, errors.New("additional data has no completed scopes")
	}
	return out, code50, nil
}
