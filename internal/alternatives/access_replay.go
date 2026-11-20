package alternatives

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"io"
	"os"
	"path/filepath"
	"time"
)

type replayEntry struct {
	Timing    *reachability.Coverage `json:"time_coverage,omitempty"`
	Origin    odsay.Point            `json:"origin"`
	Target    odsay.Point            `json:"target"`
	Kind      string                 `json:"kind"`
	File      string                 `json:"file"`
	Directory string                 `json:"recorded_directory"`
}
type AccessReplay struct {
	entries   []replayEntry
	responses []odsay.Response
	origins   []string
}

// LoadAccessReplay binds every stored response to its exact origin/destination.
// Synthetic fixtures stay explicitly synthetic. Real captures must pass the
// existing Recorded loader's metadata and raw SHA256 checks. No HTTP is used.
func LoadAccessReplay(path string) (*AccessReplay, error) {
	b, e := readAccessFile(path)
	if e != nil {
		return nil, e
	}
	var entries []replayEntry
	if json.Unmarshal(b, &entries) != nil || len(entries) == 0 || len(entries) > 4 {
		return nil, errors.New("invalid access manifest")
	}
	r := &AccessReplay{entries: entries}
	for _, entry := range entries {
		q := odsay.Query{Start: entry.Origin, End: entry.Target, Mode: "all"}
		if !q.Valid() {
			return nil, ErrInput
		}
		var response odsay.Response
		source := "synthetic_access_fixture"
		switch entry.Kind {
		case "synthetic_fixture":
			raw, e := readAccessFile(filepath.Join(filepath.Dir(path), entry.File))
			if e != nil {
				return nil, e
			}
			response, e = odsay.Parse(raw)
			if e != nil {
				return nil, e
			}
		case "odsay_recorded_response":
			saved, e := odsay.LoadRecorded(filepath.Join(filepath.Dir(path), entry.Directory))
			if e != nil {
				return nil, e
			}
			response, source, e = saved.Search(context.Background(), q)
			captured := saved.Provenance().CapturedAt
			walking := true
			r.entries[len(r.responses)].Timing = &reachability.Coverage{Walking: &walking, CapturedAt: &captured, Evidence: "recorded_provider_sections_only_wait_coverage_unverified"}
			if e != nil {
				return nil, e
			}
		default:
			return nil, errors.New("unknown access fixture provenance")
		}
		for _, prior := range r.entries[:len(r.responses)] {
			if prior.Origin == entry.Origin && prior.Target == entry.Target {
				return nil, errors.New("duplicate access coordinate binding")
			}
		}
		r.responses = append(r.responses, response)
		r.origins = append(r.origins, source)
	}
	return r, nil
}
func readAccessFile(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("access fixture unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if e != nil || len(b) > 4<<20 {
		return nil, errors.New("invalid access fixture")
	}
	return b, nil
}
func (r *AccessReplay) Search(ctx context.Context, q odsay.Query) (odsay.Response, string, error) {
	if ctx.Err() != nil {
		return odsay.Response{}, "", ctx.Err()
	}
	for i, e := range r.entries {
		if q.Mode == "all" && q.Start == e.Origin && q.End == e.Target {
			return r.responses[i], r.origins[i], nil
		}
	}
	return odsay.Response{}, "access_recording_missing", odsay.ErrInput
}

func (r *AccessReplay) TimingFor(q odsay.Query) *reachability.Coverage {
	for _, e := range r.entries {
		if e.Origin == q.Start && e.Target == q.End {
			return e.Timing
		}
	}
	return nil
}

// Snapshots reuses explicitly configured recordings without making them live or
// resetting their capture timestamps. Both access and segment datasets use this loader.
func (r *AccessReplay) Snapshots() []odsay.Selection {
	if r == nil {
		return nil
	}
	out := make([]odsay.Selection, 0, len(r.responses))
	for i, response := range r.responses {
		var captured time.Time
		if r.entries[i].Timing != nil && r.entries[i].Timing.CapturedAt != nil {
			captured = *r.entries[i].Timing.CapturedAt
		}
		out = append(out, odsay.Selection{Response: response, Query: odsay.Query{Start: r.entries[i].Origin, End: r.entries[i].Target, Mode: "all"}, Origin: r.origins[i], CapturedAt: captured})
	}
	return out
}
