package repository

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/stopmapping"
	"strings"
)

type JourneyReferences struct {
	db         DB
	statistics *Congestion
}

func NewJourneyReferences(db DB) *JourneyReferences {
	return &JourneyReferences{db: db, statistics: NewCongestion(db)}
}
func (j *JourneyReferences) ValidateReference(ctx context.Context, q odsay.Query) error {
	var exists bool
	if j.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM congestion_analysis_sets a JOIN congestion_analysis_regions r USING(analysis_set_id) WHERE a.analysis_set_id=$1 AND r.service_date=$2::text::date)`, q.Set, q.Date).Scan(&exists) != nil {
		return ErrQuery
	}
	if !exists {
		return ErrAnalysisSet
	}
	return nil
}

type providerVisit struct {
	ID       int64   `json:"id"`
	Sequence int     `json:"sequence"`
	Name     string  `json:"name"`
	ARS      *string `json:"ars"`
	Via      bool    `json:"via"`
}

func (j *JourneyReferences) find(ctx context.Context, route int64, id, name, ars string, provider *int) (*providerVisit, string, error) {
	if provider == nil || *provider != 2 {
		return nil, "provider_namespace_unverified", nil
	}
	if id == "" {
		return nil, "local_station_id_missing", nil
	}
	rows, e := j.db.Query(ctx, `SELECT v.route_stop_id,v.stop_sequence,v.stop_name,v.mobile_no,v.is_via_name FROM qgis_route_stops_current v JOIN stop_external_ids s ON s.stop_id=v.stop_id AND s.source_id='gbis' WHERE v.route_id=$1 AND s.external_stop_id=$2 ORDER BY v.stop_sequence`, route, id)
	if e != nil {
		return nil, "", ErrQuery
	}
	defer rows.Close()
	visits := []providerVisit{}
	for rows.Next() {
		var v providerVisit
		if rows.Scan(&v.ID, &v.Sequence, &v.Name, &v.ARS, &v.Via) != nil {
			return nil, "", ErrQuery
		}
		visits = append(visits, v)
	}
	if rows.Err() != nil {
		return nil, "", ErrQuery
	}
	return selectProviderVisit(visits, name, ars)
}

// Candidates have already been scoped by official GBIS ID and route in find.
func selectProviderVisit(visits []providerVisit, name, ars string) (*providerVisit, string, error) {
	if len(visits) == 0 {
		return nil, "local_station_id_not_in_route", nil
	}
	if len(visits) != 1 {
		return nil, "ambiguous_route_visit", nil
	}
	v := visits[0]
	if stopmapping.CompareName(v.Name) != stopmapping.CompareName(name) {
		return nil, "station_name_conflict", nil
	}
	if v.ARS != nil && strings.TrimSpace(*v.ARS) != "" && ars != "" && ars != "0" && ars != "~" {
		if strings.ReplaceAll(strings.TrimSpace(*v.ARS), "-", "") != strings.ReplaceAll(strings.TrimSpace(ars), "-", "") {
			return nil, "ars_conflict", nil
		}
	}
	return &v, "matched_declared_gbis_local_id", nil
}
func (j *JourneyReferences) Resolve(ctx context.Context, q odsay.Query, lane odsay.Lane, leg odsay.SubPath) (odsay.BridgeResult, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	m := odsay.BridgeResult{Status: "unresolved", BoardingStatus: "not_checked", AlightingStatus: "not_checked", Notes: []string{"reference_only_not_boarding_probability", "boarding_permission_unverified", "historical_mapping_status_unverified", "ODsay_stationID_is_not_GBIS_stationId"}}
	if lane.Provider == nil || *lane.Provider != 2 || lane.LocalID == "" {
		m.Status = "route_provider_namespace_unverified"
		return m, nil
	}
	rows, e := j.db.Query(ctx, `SELECT r.route_id FROM bus_routes r JOIN route_external_ids x USING(route_id) WHERE x.source_id='gbis' AND x.external_route_id=$1 AND r.route_number=$2 AND r.route_type_raw IN ('11','14')`, lane.LocalID, lane.BusNumber)
	if e != nil {
		return m, ErrQuery
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil {
			rows.Close()
			return m, ErrQuery
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return m, ErrQuery
	}
	if len(ids) != 1 {
		m.Status = "route_identity_missing_or_ambiguous"
		return m, nil
	}
	routeID := ids[0]
	m.RouteID = &routeID
	start, status, e := j.find(ctx, routeID, leg.StartLocal, leg.StartName, leg.StartARS, leg.StartProvider)
	if e != nil {
		return m, e
	}
	m.BoardingStatus = status
	end, status, e := j.find(ctx, routeID, leg.EndLocal, leg.EndName, leg.EndARS, leg.EndProvider)
	if e != nil {
		return m, e
	}
	m.AlightingStatus = status
	if start == nil {
		m.Status = "boarding_mapping_unverified"
		return m, nil
	}
	m.BoardingVisitID = &start.ID
	if end != nil {
		m.AlightingVisitID = &end.ID
		if end.Sequence <= start.Sequence {
			m.Status = "visit_order_conflict"
			return m, nil
		}
	} else {
		m.Notes = append(m.Notes, "alighting_identity_unverified")
	}
	if start.Via {
		m.Notes = append(m.Notes, "via_point_requires_boarding_review")
	}
	reference, e := j.statistics.VisitProfile(ctx, routeID, start.ID, q.Date, q.Set, q.RawTimeCode)
	if errors.Is(e, ErrAnalysisSet) {
		m.Status = "reference_unavailable_for_selected_set"
		return m, nil
	}
	if e != nil {
		return m, e
	}
	var record struct {
		Status  string `json:"status"`
		Mapping string `json:"mapping_status"`
	}
	if json.Unmarshal(reference, &record) != nil {
		return m, ErrInvalidData
	}
	m.Status = "reference_" + record.Status
	m.Reference = reference
	return m, nil
}
