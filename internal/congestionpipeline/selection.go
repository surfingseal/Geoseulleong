package congestionpipeline

import (
	"encoding/json"
	"errors"
)

// SelectBatches prevents summing repeated snapshots of one geographic query.
// Conflicting snapshots require an explicit chosen ID; observations inside a snapshot are untouched.
func SelectBatches(input []Batch, chosen map[string]string) ([]Batch, error) {
	groups := map[string][]Batch{}
	order := []string{}
	for _, b := range input {
		checked, e := Build(b.Scope, b.Pages)
		if e != nil || checked.ID != b.ID || checked.Total != b.Total || !b.Complete {
			return nil, errors.New("selection input invalid")
		}
		key, _ := json.Marshal(b.Scope)
		k := string(key)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		duplicate := false
		for _, old := range groups[k] {
			if old.ID == b.ID {
				duplicate = true
			}
		}
		if !duplicate {
			groups[k] = append(groups[k], checked)
		}
	}
	out := []Batch{}
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			if id := chosen[k]; id != "" && id != g[0].ID {
				return nil, errors.New("chosen snapshot missing")
			}
			out = append(out, g[0])
			continue
		}
		id := chosen[k]
		found := false
		for _, b := range g {
			if b.ID == id {
				out = append(out, b)
				found = true
			}
		}
		if !found {
			return nil, errors.New("multiple snapshots require explicit batch selection")
		}
	}
	return out, nil
}
