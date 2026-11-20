package collectionpreview

import (
	"errors"
	"geoseulleong/internal/congestionpipeline"
	"geoseulleong/internal/congestionplan"
)

// ImportLegacy reuses immutable scope/raw/count/hash validation; it does not loosen legacy approval gates.
// Ingested IDs must come from a separate read-only DB inventory; an absent ID is not assumed stored.
func ImportLegacy(p congestionplan.Plan, ingested map[string]bool) ([]Snapshot, error) {
	if p.Validate() != nil {
		return nil, errors.New("legacy plan failed immutable validation")
	}
	out := []Snapshot{}
	for _, b := range p.Batches {
		checked, e := congestionpipeline.Build(b.Scope, b.Pages)
		if e != nil || checked.ID != b.ID {
			return nil, errors.New("legacy raw batch mismatch")
		}
		n := int64(checked.Total)
		s := Snapshot{Scope: b.Scope, ID: b.ID, State: "complete", Total: &n, Pages: []SavedPage{}, Ingested: ingested[b.ID]}
		for _, page := range checked.Pages {
			s.Pages = append(s.Pages, SavedPage{Number: int64(page.Number), Rows: int64(len(page.Rows)), SHA: page.SHA})
		}
		out = append(out, s)
	}
	for _, q := range p.Queries {
		out = append(out, Snapshot{Scope: q.Scope, ID: q.ID, State: "no_data_for_query", Pages: []SavedPage{}, Ingested: ingested[q.ID]})
	}
	return out, nil
}
