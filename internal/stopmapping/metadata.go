package stopmapping

import (
	"encoding/hex"
	"geoseulleong/internal/stcismetadata"
	"strings"
)

// Observation identifies the actual provider record; its region and sequence are not inferred.
type Observation struct {
	StopID   string `json:"stcis_stop_id"`
	Sequence int    `json:"stcis_sequence"`
	Route    string `json:"stcis_route_id"`
	Province string `json:"province"`
	District string `json:"district"`
}
type MetadataReview struct {
	Observation              Observation               `json:"observation"`
	OfficialRouteRows        []stcismetadata.RouteStop `json:"official_route_rows"`
	OfficialStopRows         []stcismetadata.Stop      `json:"official_stop_rows"`
	Result                   Result                    `json:"mapping"`
	IDCorrespondenceVerified bool                      `json:"id_correspondence_verified"`
	RouteMembershipVerified  bool                      `json:"route_membership_verified"`
	RegionVerified           bool                      `json:"region_verified"`
	NameExact                bool                      `json:"name_exact"`
	ARSMatch                 *bool                     `json:"ars_match"`
	DirectionVerified        bool                      `json:"direction_verified"`
	CoordinatesVerified      bool                      `json:"coordinates_verified"`
	SequenceOffset           *int                      `json:"observed_sequence_offset"`
	VerificationScope        string                    `json:"verification_scope"`
	HistoricalValidity       string                    `json:"historical_validity"`
}

// ReviewMetadata requires official BIS cross-reference, route membership and consistent region/name.
// A qualified non-stopping name difference is retained for review, never silently normalized away.
func ReviewMetadata(o Observation, routeRows []stcismetadata.RouteStop, stops []stcismetadata.Stop, visits []Visit, mobileNumbers map[string]string, e Evidence) MetadataReview {
	out := MetadataReview{Observation: o, OfficialRouteRows: []stcismetadata.RouteStop{}, OfficialStopRows: []stcismetadata.Stop{}, Result: Result{Status: "unresolved", Method: "official_metadata_review", Candidates: []Visit{}, Reasons: []string{}}, VerificationScope: "current_STCIS_metadata_to_stored_GBIS_visit", HistoricalValidity: "20261001_validity_not_verified"}
	for _, r := range routeRows {
		if r.ID == o.StopID && r.Route == o.Route && r.Sequence == o.Sequence {
			out.OfficialRouteRows = append(out.OfficialRouteRows, r)
		}
	}
	for _, s := range stops {
		if s.ID == o.StopID {
			out.OfficialStopRows = append(out.OfficialStopRows, s)
		}
	}
	if len(out.OfficialRouteRows) == 0 || len(out.OfficialStopRows) == 0 {
		out.Result.Reasons = append(out.Result.Reasons, "official_metadata_missing")
		return out
	}
	if len(out.OfficialRouteRows) != 1 || len(out.OfficialStopRows) != 1 {
		out.Result.Status = "ambiguous"
		out.Result.Reasons = append(out.Result.Reasons, "duplicate_provider_metadata")
		return out
	}
	r, s := out.OfficialRouteRows[0], out.OfficialStopRows[0]
	if r.Province != o.Province || r.District != o.District || s.Province != o.Province || s.District != o.District || r.Dong != "" && s.Dong != "" && r.Dong != s.Dong {
		out.Result.Status = "rejected"
		out.Result.Reasons = append(out.Result.Reasons, "region_conflict")
		return out
	}
	out.RegionVerified = true
	out.RouteMembershipVerified = true
	if r.RouteNumber != "M5121" || norm(r.Name) != norm(s.Name) {
		out.Result.Status = "rejected"
		out.Result.Reasons = append(out.Result.Reasons, "route_or_stop_name_conflict")
		return out
	}
	source := Source{ID: o.StopID, Route: o.Route, Name: s.Name, Sequence: o.Sequence, BISID: s.BISID}
	e.RouteConfirmed = true

	// The stored M5121 datasets explicitly use STCIS zero-based and GBIS one-based visits.
	// Use this as an additional consistency check, never as the identity join.
	for _, v := range visits {
		rawDiff := strings.Join(strings.Fields(s.Name), "") != strings.Join(strings.Fields(v.Name), "")
		if rawDiff && norm(s.Name) == norm(v.Name) && s.BISID != "" && v.ExternalID == s.BISID && v.Sequence == o.Sequence+1 {
			expected := v.Sequence
			e.ExpectedVisitSequence = &expected
		}
	}

	out.Result = Match(source, visits, e)
	exact := []Visit{}
	for _, v := range visits {
		if s.BISID != "" && v.ExternalID == s.BISID {
			exact = append(exact, v)
		}
	}
	if len(exact) == 1 {
		v := exact[0]
		offset := v.Sequence - o.Sequence
		out.SequenceOffset = &offset
		out.NameExact = strings.Join(strings.Fields(v.Name), "") == strings.Join(strings.Fields(s.Name), "")
		// Namespace evidence is explicitly supplied from official documentation and the GBIS external-id table.
		_, hashErr := hex.DecodeString(e.ResponseSHA)
		out.IDCorrespondenceVerified = hashErr == nil && e.BISNamespaceConfirmed && strings.HasPrefix(e.OfficialReference, "https://") && len(e.ResponseSHA) == 64
		ars := strings.TrimSpace(s.ARS)
		gbisARS := strings.TrimSpace(mobileNumbers[v.ExternalID])
		if ars != "" && ars != "~" && gbisARS != "" && gbisARS != "~" {
			same := ars == gbisARS
			out.ARSMatch = &same
			if !same {
				out.Result.Status = "rejected"
				out.Result.Visit = nil
				out.Result.Reasons = append(out.Result.Reasons, "ars_conflict")
			}
		}
		if !out.NameExact && out.IDCorrespondenceVerified && len(out.Result.Reasons) == 1 && out.Result.Reasons[0] == "name_conflict" {
			out.Result.Status = "candidate"
			out.Result.Method = "official_id_name_difference_review"
			out.Result.Visit = nil
			out.Result.Reasons = []string{"official_name_difference_requires_review"}
		}
	}
	return out
}
