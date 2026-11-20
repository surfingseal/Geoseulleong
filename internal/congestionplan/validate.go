package congestionplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/congestionpipeline"
	"geoseulleong/internal/routecongestion"
	"time"
)

func (p Plan) canonical() ([]byte, error) {
	selected := []map[string]any{}
	for _, b := range p.Batches {
		selected = append(selected, map[string]any{"scope": b.Scope, "batch_id": b.ID, "records": b.Total})
	}
	for _, q := range p.Queries {
		selected = append(selected, map[string]any{"scope": q.Scope, "query_id": q.ID, "status": q.Status})
	}
	contents := map[string]any{"version": "m5121-storage-v1", "internal_route_id": p.RouteID, "date": p.Date, "external_route_mapping": "user-confirmed M5121 GBIS234001317 / STCIS41002905; not same ID namespace", "selected": selected, "mappings": p.Mappings, "interpretation_policy": congestionpipeline.Policy}
	if len(p.MappingProvenance) > 0 {
		if !json.Valid(p.MappingProvenance) {
			return nil, errors.New("mapping provenance invalid")
		}
		contents["mapping_provenance"] = p.MappingProvenance
	}
	return json.Marshal(contents)
}
func (p *Plan) Seal() error {
	b, e := p.canonical()
	if e != nil {
		return e
	}
	p.Manifest = b
	p.ID = hash(b)
	return nil
}
func (p Plan) Validate() error {
	if p.RouteID <= 0 {
		return errors.New("invalid internal route")
	}
	if _, e := time.Parse("20060102", p.Date); e != nil {
		return e
	}
	b, e := p.canonical()
	var compact bytes.Buffer
	compactErr := json.Compact(&compact, p.Manifest)
	if e != nil || compactErr != nil || hash(b) != p.ID || string(b) != compact.String() {
		return errors.New("plan fields do not match immutable manifest")
	}
	selected, e := congestionpipeline.SelectBatches(p.Batches, nil)
	if e != nil || len(selected) != len(p.Batches) {
		return errors.New("ambiguous analysis batches")
	}
	scopes := map[string]bool{}
	observed := map[string]bool{}
	for _, b := range p.Batches {
		key, _ := json.Marshal(b.Scope)
		if scopes[string(key)] {
			return errors.New("duplicate query scope")
		}
		scopes[string(key)] = true
		if b.Scope.Date != p.Date {
			return errors.New("selection date mismatch")
		}
		for _, page := range b.Pages {
			for _, row := range page.Rows {
				observed[fmt.Sprint(b.Scope.Route, "/", congestionpipeline.Value(row, "sttn_id"), "/", congestionpipeline.Value(row, "sttn_seq"))] = true
			}
		}
	}
	for _, q := range p.Queries {
		parsed, e := routecongestion.Parse(q.Raw)
		idInput, _ := json.Marshal(struct {
			Scope routecongestion.Scope
			Hash  string
		}{q.Scope, q.SHA})
		if q.HTTP != 200 || !errors.Is(e, routecongestion.ErrNoData) || parsed.Code != "50" || q.Code != "50" || q.Status != "no_data_for_query" || q.SHA != hash(q.Raw) || q.ID != hash(idInput) || !q.Scope.Allowed() || q.Scope.Date != p.Date {
			return errors.New("query outcome invalid")
		}
		key, _ := json.Marshal(q.Scope)
		if scopes[string(key)] {
			return errors.New("duplicate query scope")
		}
		scopes[string(key)] = true
	}
	seen := map[string]bool{}
	for _, m := range p.Mappings {
		key := fmt.Sprint(m.Source.Route, "/", m.Source.ID, "/", m.Source.Sequence)
		if !observed[key] || seen[key] {
			return errors.New("mapping selection mismatch")
		}
		seen[key] = true
		for _, v := range m.Result.Candidates {
			if v.RouteID != p.RouteID {
				return errors.New("mapping candidate belongs to another internal route")
			}
		}
	}
	if len(seen) != len(observed) {
		return errors.New("observed mapping missing")
	}
	return nil
}
