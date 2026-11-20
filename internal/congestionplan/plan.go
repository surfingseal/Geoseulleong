// Package congestionplan prepares an immutable, offline ingest selection and mapping evidence.
package congestionplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/congestionpipeline"
	"geoseulleong/internal/routecongestion"
	"geoseulleong/internal/stopmapping"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

type Query struct {
	ID     string                `json:"query_id"`
	Scope  routecongestion.Scope `json:"scope"`
	HTTP   int                   `json:"http_status"`
	Code   string                `json:"api_code"`
	Status string                `json:"status"`
	SHA    string                `json:"response_sha256"`
	Raw    []byte                `json:"raw_response_base64"`
	At     time.Time             `json:"collected_at"`
}
type Mapping struct {
	Source   stopmapping.Source   `json:"source"`
	Result   stopmapping.Result   `json:"result"`
	Evidence stopmapping.Evidence `json:"evidence"`
}
type Plan struct {
	MappingProvenance json.RawMessage            `json:"mapping_provenance,omitempty"`
	ID                string                     `json:"analysis_set_id"`
	RouteID           int64                      `json:"internal_route_id"`
	Date              string                     `json:"service_date"`
	Batches           []congestionpipeline.Batch `json:"batches"`
	Queries           []Query                    `json:"query_outcomes"`
	Mappings          []Mapping                  `json:"mappings"`
	Manifest          json.RawMessage            `json:"manifest"`
}

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func Prepare(project string, routeID int64, visits []stopmapping.Visit) (Plan, error) {
	p := Plan{RouteID: routeID, Date: "20261001"}
	for _, path := range []string{"results/full-congestion-20261001/41-41117/batch.json", "results/full-congestion-20261001/11-11140/batch.json", "results/additional-congestion-20261001/11-11170/batch.json"} {
		raw, e := os.ReadFile(filepath.Join(project, path))
		if e != nil {
			return p, errors.New("existing batch missing")
		}
		var b congestionpipeline.Batch
		if json.Unmarshal(raw, &b) != nil {
			return p, errors.New("invalid batch")
		}
		p.Batches = append(p.Batches, b)
	}
	var e error
	p.Batches, e = congestionpipeline.SelectBatches(p.Batches, nil)
	if e != nil {
		return p, e
	}
	if len(p.Batches) != 3 {
		return p, errors.New("expected 3 distinct positive scopes")
	}
	records := 0
	for _, b := range p.Batches {
		if b.Scope.Route != "41002905" || b.Scope.Date != p.Date {
			return p, errors.New("unexpected batch scope")
		}
		records += b.Total
	}
	if records != 1330 {
		return p, errors.New("expected 1330 actual records")
	}
	for _, pair := range [][2]string{{"11", "11110"}, {"11", "11650"}, {"11", "11680"}, {"41", "41131"}, {"41", "41135"}, {"41", "41463"}, {"41", "41465"}} {
		path := filepath.Join(project, "results/additional-congestion-20261001", pair[0]+"-"+pair[1], "page-0001.state.json")
		b, e := os.ReadFile(path)
		if e != nil {
			return p, e
		}
		var state congestionpipeline.State
		s := routecongestion.Scope{Date: p.Date, Province: pair[0], District: pair[1], Route: "41002905"}
		if json.Unmarshal(b, &state) != nil || state.Scope != s || state.Number != 1 || state.HTTP != 200 || state.Status != "failed" {
			return p, errors.New("query outcome mismatch")
		}
		parsed, e := routecongestion.Parse(state.Raw)
		if !errors.Is(e, routecongestion.ErrNoData) || parsed.Code != "50" || !json.Valid(state.Raw) {
			return p, errors.New("expected original no-data response")
		}
		h := hash(state.Raw)
		idInput, _ := json.Marshal(struct {
			Scope routecongestion.Scope
			Hash  string
		}{s, h})
		p.Queries = append(p.Queries, Query{ID: hash(idInput), Scope: s, HTTP: state.HTTP, Code: parsed.Code, Status: "no_data_for_query", SHA: h, Raw: state.Raw, At: state.At})
	}
	sources := map[string]stopmapping.Source{}
	for _, b := range p.Batches {
		for _, page := range b.Pages {
			for _, raw := range page.Rows {
				id := congestionpipeline.Value(raw, "sttn_id")
				seq, e := strconv.Atoi(congestionpipeline.Value(raw, "sttn_seq"))
				if e != nil {
					return p, e
				}
				key := fmt.Sprintf("%s/%d", id, seq)
				sources[key] = stopmapping.Source{ID: id, Sequence: seq, Route: b.Scope.Route}
			}
		}
	}
	keys := []string{}
	for k := range sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		s := sources[key]
		p.Mappings = append(p.Mappings, Mapping{Source: s, Result: stopmapping.Match(s, visits, stopmapping.Evidence{})})
	}
	if len(p.Mappings) != 22 {
		return p, errors.New("expected 22 observed source stop visits")
	}
	selected := []map[string]any{}
	for _, b := range p.Batches {
		selected = append(selected, map[string]any{"scope": b.Scope, "batch_id": b.ID, "records": b.Total})
	}
	for _, q := range p.Queries {
		selected = append(selected, map[string]any{"scope": q.Scope, "query_id": q.ID, "status": q.Status})
	}
	manifest, e := json.Marshal(map[string]any{"version": "m5121-storage-v1", "internal_route_id": routeID, "date": p.Date, "external_route_mapping": "user-confirmed M5121 GBIS234001317 / STCIS41002905; not same ID namespace", "selected": selected, "mappings": p.Mappings, "interpretation_policy": congestionpipeline.Policy})
	if e != nil {
		return p, e
	}
	p.Manifest = manifest
	p.ID = hash(manifest)
	return p, nil
}
