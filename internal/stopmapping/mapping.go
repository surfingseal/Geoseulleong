// Package stopmapping keeps evidence and hypotheses separate from verified visit links.
package stopmapping

import (
	"encoding/hex"
	"math"
	"strings"
)

type Visit struct {
	ID         int64  `json:"route_stop_id"`
	RouteID    int64  `json:"route_id"`
	StopID     string `json:"stop_id"`
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	Sequence   int    `json:"sequence"`
	Direction  string `json:"direction,omitempty"`
	Via        bool   `json:"is_via"`
}
type Source struct {
	ID        string `json:"id"`
	Route     string `json:"route"`
	Name      string `json:"name"`
	Sequence  int    `json:"sequence"`
	Direction string `json:"direction,omitempty"`
	BISID     string `json:"bis_id,omitempty"`
}
type Evidence struct {
	OfficialReference      string   `json:"official_reference"`
	ResponseSHA            string   `json:"response_sha256"`
	BISNamespaceConfirmed  bool     `json:"bis_namespace_confirmed"`
	RouteConfirmed         bool     `json:"route_confirmed"`
	ExpectedVisitSequence  *int     `json:"expected_visit_sequence,omitempty"`
	DistanceM              *float64 `json:"distance_m,omitempty"`
	CoordinateCRSConfirmed bool     `json:"coordinate_crs_confirmed"`
}
type Result struct {
	Status     string   `json:"status"`
	Method     string   `json:"method"`
	Visit      *Visit   `json:"verified_visit"`
	Candidates []Visit  `json:"candidates"`
	Reasons    []string `json:"reasons"`
}

// CompareName ignores the central-stop annotation and normalizes non-stopping
// qualifiers only for comparison. Provider names and boarding permission are unchanged.
func CompareName(s string) string {
	s = strings.Join(strings.Fields(s), "")
	s = strings.ReplaceAll(s, "(중)", "")
	return strings.ReplaceAll(s, "(미정차)", "(경유)")
}
func norm(s string) string { return CompareName(s) }

func Match(s Source, visits []Visit, e Evidence) Result {
	r := Result{Status: "unresolved", Method: "insufficient_evidence", Candidates: []Visit{}, Reasons: []string{}}
	if s.ID == "" || s.Route == "" {
		return r
	}
	for _, v := range visits {
		// Sequence+1 is a candidate generator, never independent verification.
		if (s.BISID != "" && v.ExternalID == s.BISID) || (s.Name != "" && norm(v.Name) == norm(s.Name)) || (s.Name == "" && v.Sequence == s.Sequence+1) {
			r.Candidates = append(r.Candidates, v)
		}
	}
	if len(r.Candidates) == 0 {
		r.Reasons = append(r.Reasons, "no_matching_metadata")
		return r
	}
	if s.BISID != "" {
		exact := []Visit{}
		for _, v := range r.Candidates {
			if v.ExternalID == s.BISID {
				exact = append(exact, v)
			}
		}
		if len(exact) == 0 {
			r.Status = "rejected"
			r.Reasons = append(r.Reasons, "official_id_mismatch")
			return r
		}
		r.Candidates = exact
	}
	if len(r.Candidates) > 1 {
		r.Status = "ambiguous"
		r.Reasons = append(r.Reasons, "multiple_visits_or_stops")
		return r
	}
	v := r.Candidates[0]
	for _, conflict := range []struct {
		bad    bool
		reason string
	}{{s.Name != "" && norm(v.Name) != norm(s.Name), "name_conflict"}, {s.Direction != "" && v.Direction != "" && s.Direction != v.Direction, "direction_conflict"}, {e.ExpectedVisitSequence != nil && *e.ExpectedVisitSequence != v.Sequence, "sequence_conflict"}, {e.CoordinateCRSConfirmed && e.DistanceM != nil && (math.IsNaN(*e.DistanceM) || math.IsInf(*e.DistanceM, 0) || *e.DistanceM < 0 || *e.DistanceM > 50), "coordinate_conflict"}} {
		if conflict.bad {
			r.Status = "rejected"
			r.Reasons = append(r.Reasons, conflict.reason)
			return r
		}
	}

	qualifierOnly := strings.Join(strings.Fields(s.Name), "") != strings.Join(strings.Fields(v.Name), "") && norm(s.Name) == norm(v.Name)
	if qualifierOnly && e.ExpectedVisitSequence == nil {
		r.Status = "candidate"
		r.Method = "name_normalized_sequence_review"
		r.Reasons = append(r.Reasons, "visit_sequence_evidence_required")
		return r
	}
	_, shaErr := hex.DecodeString(e.ResponseSHA)
	if shaErr == nil && s.BISID != "" && v.ExternalID == s.BISID && e.RouteConfirmed && e.BISNamespaceConfirmed && strings.HasPrefix(e.OfficialReference, "https://") && len(e.ResponseSHA) == 64 {
		r.Status = "verified"
		r.Method = "official_bis_id_and_route"
		r.Visit = &v
		return r
	}
	r.Status = "candidate"
	r.Method = "name_or_sequence_hypothesis"
	r.Reasons = append(r.Reasons, "official_provider_metadata_required")
	return r
}
