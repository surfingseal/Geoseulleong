// Package odsay keeps provider identifiers, mock provenance, and congestion references distinct.
package odsay

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/boarding"
	"math"
	"time"
)

var ErrDisabled = errors.New("ODsay live requests disabled pending approval")
var ErrProvider = errors.New("ODsay provider error")
var ErrSchema = errors.New("ODsay response invalid or unsupported")
var ErrUnsupportedMode = errors.New("routing mode unsupported by provider")
var ErrInput = errors.New("invalid routing input")
var ErrBudget = errors.New("ODsay request budget exhausted")

type Point struct {
	Lon float64 `json:"lon"`
	Lat float64 `json:"lat"`
}
type Query struct {
	EvaluationModel     string `json:"evaluation_model,omitempty"`
	DepartureAt         string `json:"departure_at,omitempty"`
	BoardingServiceDate string `json:"boarding_service_date,omitempty"`
	Start               Point  `json:"start"`
	End                 Point  `json:"end"`
	Mode                string `json:"mode"`
	Date                string `json:"reference_date"`
	Set                 string `json:"analysis_set_id"`
	RawTimeCode         string `json:"reference_tzon,omitempty"`
}

func (q Query) Valid() bool {
	if q.EvaluationModel != "" && q.EvaluationModel != "travel_time_only_v1" && q.EvaluationModel != "boarding_aware_v1" {
		return false
	}
	if q.DepartureAt != "" {
		if _, e := time.Parse(time.RFC3339, q.DepartureAt); e != nil {
			return false
		}
	}
	if q.BoardingServiceDate != "" {
		if _, e := time.Parse("2006-01-02", q.BoardingServiceDate); e != nil {
			return false
		}
	}
	for _, p := range []Point{q.Start, q.End} {
		if math.IsNaN(p.Lon) || math.IsInf(p.Lon, 0) || math.IsNaN(p.Lat) || math.IsInf(p.Lat, 0) || p.Lon < -180 || p.Lon > 180 || p.Lat < -90 || p.Lat > 90 {
			return false
		}
	}
	return q.Start != q.End && (q.Mode == "all" || q.Mode == "bus" || q.Mode == "subway" || q.Mode == "mixed" || q.Mode == "walk")
}

type ID string

func (s *ID) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var v string
	if json.Unmarshal(b, &v) == nil {
		*s = ID(v)
		return nil
	}
	var n json.Number
	if json.Unmarshal(b, &n) != nil {
		return ErrSchema
	}
	*s = ID(n.String())
	return nil
}

type Lane struct {
	Namespace  string `json:"provider_namespace,omitempty"`
	Kind       string `json:"provider_type,omitempty"`
	Type       *int   `json:"type,omitempty"`
	BusNumber  string `json:"busNo,omitempty"`
	ODsayID    ID     `json:"busID,omitempty"`
	LocalID    string `json:"busLocalBlID,omitempty"`
	Provider   *int   `json:"busProviderCode,omitempty"`
	Name       string `json:"name,omitempty"`
	SubwayCode *int   `json:"subwayCode,omitempty"`
}
type SubPath struct {
	Seconds       *int            `json:"duration_seconds,omitempty"`
	Namespace     string          `json:"provider_namespace,omitempty"`
	Details       json.RawMessage `json:"provider_details,omitempty"`
	TrafficType   int             `json:"trafficType"`
	Minutes       *float64        `json:"sectionTime"`
	Lanes         []Lane          `json:"lane,omitempty"`
	StartID       ID              `json:"startID,omitempty"`
	EndID         ID              `json:"endID,omitempty"`
	StartLocal    string          `json:"startLocalStationID,omitempty"`
	EndLocal      string          `json:"endLocalStationID,omitempty"`
	StartProvider *int            `json:"startStationProviderCode,omitempty"`
	EndProvider   *int            `json:"endStationProviderCode,omitempty"`
	StartName     string          `json:"startName,omitempty"`
	EndName       string          `json:"endName,omitempty"`
	StartARS      string          `json:"startArsID,omitempty"`
	EndARS        string          `json:"endArsID,omitempty"`
	StartX        *float64        `json:"startX,omitempty"`
	StartY        *float64        `json:"startY,omitempty"`
	EndX          *float64        `json:"endX,omitempty"`
	EndY          *float64        `json:"endY,omitempty"`
}
type Path struct {
	Type int `json:"pathType"`
	Info struct {
		Seconds     *int     `json:"provider_total_seconds,omitempty"`
		Transfers   *int     `json:"provider_transfer_count,omitempty"`
		TotalTime   *float64 `json:"totalTime"`
		BusCount    *int     `json:"busTransitCount"`
		SubwayCount *int     `json:"subwayTransitCount"`
	} `json:"info"`
	Legs []SubPath `json:"subPath"`
}
type Response struct {
	SearchType int    `json:"searchType"`
	Paths      []Path `json:"path"`
}

func Parse(raw []byte) (Response, error) {
	var root struct {
		Result *Response       `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &root) != nil {
		return Response{}, ErrSchema
	}
	if len(root.Error) > 0 && string(root.Error) != "null" {
		return Response{}, ErrProvider
	}
	if root.Result == nil || root.Result.SearchType != 0 || root.Result.Paths == nil {
		return Response{}, ErrSchema
	}
	var fields struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &fields) != nil || fields.Result["searchType"] == nil {
		return Response{}, ErrSchema
	}
	r := *root.Result
	if len(r.Paths) > 50 {
		return Response{}, ErrSchema
	}
	for _, p := range r.Paths {
		if p.Type < 1 || p.Type > 3 || p.Info.TotalTime == nil || *p.Info.TotalTime < 0 || len(p.Legs) == 0 || len(p.Legs) > 100 {
			return Response{}, ErrSchema
		}
		hasBus, hasSubway := false, false
		for _, s := range p.Legs {
			if s.TrafficType < 1 || s.TrafficType > 3 || s.Minutes == nil || *s.Minutes < 0 {
				return Response{}, ErrSchema
			}
			if s.TrafficType == 1 {
				hasSubway = true
			}
			if s.TrafficType == 2 {
				hasBus = true
				if len(s.Lanes) == 0 || s.StartID == "" || s.EndID == "" || s.StartName == "" || s.EndName == "" {
					return Response{}, ErrSchema
				}
				for _, l := range s.Lanes {
					if l.BusNumber == "" || l.ODsayID == "" {
						return Response{}, ErrSchema
					}
				}
			}
		}
		if p.Type == 1 && (!hasSubway || hasBus) || p.Type == 2 && (!hasBus || hasSubway) || p.Type == 3 && (!hasBus || !hasSubway) {
			return Response{}, ErrSchema
		}
	}
	return r, nil
}

type Searcher interface {
	Search(context.Context, Query) (Response, string, error)
}
type BridgeResult struct {
	Status           string          `json:"status"`
	BoardingStatus   string          `json:"boarding_status"`
	AlightingStatus  string          `json:"alighting_status"`
	RouteID          *int64          `json:"internal_route_id"`
	BoardingVisitID  *int64          `json:"boarding_visit_id"`
	AlightingVisitID *int64          `json:"alighting_visit_id"`
	Reference        json.RawMessage `json:"congestion_reference,omitempty"`
	Notes            []string        `json:"notes"`
}
type ReferenceReader interface {
	Resolve(context.Context, Query, Lane, SubPath) (BridgeResult, error)
}
type JourneyStop struct {
	ODsayID  ID       `json:"odsay_station_id"`
	LocalID  string   `json:"local_station_id,omitempty"`
	Provider *int     `json:"provider_code,omitempty"`
	Name     string   `json:"name"`
	ARS      string   `json:"ars_id,omitempty"`
	Lon      *float64 `json:"lon,omitempty"`
	Lat      *float64 `json:"lat,omitempty"`
}

type BusOption struct {
	BoardingAnalysis *boarding.Analysis `json:"boarding_analysis,omitempty"`
	CongestionStatus string             `json:"congestion_status"`
	CongestionReason string             `json:"congestion_reason"`
	Lane             Lane               `json:"lane"`
	M5121Candidate   bool               `json:"m5121_candidate"`
	Mapping          *BridgeResult      `json:"mapping,omitempty"`
}
type Leg struct {
	Index           int          `json:"leg_index"`
	DurationSeconds *int         `json:"duration_seconds,omitempty"`
	Kind            string       `json:"type"`
	DurationMinutes float64      `json:"duration_minutes"`
	Boarding        *JourneyStop `json:"boarding,omitempty"`
	Alighting       *JourneyStop `json:"alighting,omitempty"`
	ProviderData    SubPath      `json:"provider_segment"`
	Options         []BusOption  `json:"bus_options,omitempty"`
}
type Candidate struct {
	PathIndex           int        `json:"path_index"`
	TotalSeconds        *int       `json:"provider_total_seconds,omitempty"`
	ProviderTransfers   *int       `json:"provider_transfer_count,omitempty"`
	RecommendationRank  int        `json:"recommendation_rank"`
	Evaluation          Evaluation `json:"evaluation"`
	Index               int        `json:"provider_index"`
	PathType            int        `json:"path_type"`
	TotalMinutes        float64    `json:"provider_total_minutes"`
	TransferCount       int        `json:"derived_transfer_count"`
	TransferMethod      string     `json:"transfer_count_method"`
	ProviderBusCount    *int       `json:"provider_bus_count"`
	ProviderSubwayCount *int       `json:"provider_subway_count"`
	ContainsM5121       bool       `json:"contains_m5121_option"`
	Legs                []Leg      `json:"legs"`
}
type Candidates struct {
	SearchID        string            `json:"search_id,omitempty"`
	SearchExpiresAt *time.Time        `json:"search_expires_at,omitempty"`
	Source          *SourceInfo       `json:"source,omitempty"`
	Origin          string            `json:"data_origin"`
	Query           Query             `json:"query"`
	Paths           []Candidate       `json:"candidates"`
	Policy          map[string]string `json:"policy"`
}
type Service struct {
	Selections      *Selections
	BoardingWeights *BoardingWeights
	Searcher        Searcher
	Reader          ReferenceReader
	Boarding        *boarding.Service
}

func (s *Service) Candidates(ctx context.Context, q Query) (Candidates, error) {
	out := Candidates{Query: q, Paths: []Candidate{}, Policy: map[string]string{"ranking": "provider_order_unchanged", "congestion_use": "historical_reference_only", "time_matching": "not_performed", "tzon_mapping_status": "unverified", "boarding_failure_probability": "not_computed", "additional_delay": "not_computed"}}
	if !q.Valid() || (q.EvaluationModel == "boarding_aware_v1" && (s.BoardingWeights == nil || s.BoardingWeights.Validate() != nil)) {
		return out, ErrInput
	}
	if s.Searcher == nil {
		return out, ErrDisabled
	}
	if v, ok := s.Reader.(interface {
		ValidateReference(context.Context, Query) error
	}); ok {
		if e := v.ValidateReference(ctx, q); e != nil {
			return out, e
		}
	}
	raw, origin, e := s.Searcher.Search(ctx, q)
	out.Origin = origin
	if source, ok := s.Searcher.(interface{ Provenance() SourceInfo }); ok {
		info := source.Provenance()
		out.Source = &info
	}
	if e != nil {
		return out, e
	}
	for i, p := range raw.Paths {
		if !PathAllowed(q.Mode, p.Type) {
			continue
		}
		c := Candidate{PathIndex: i, TotalSeconds: p.Info.Seconds, ProviderTransfers: p.Info.Transfers, Index: i, PathType: p.Type, TotalMinutes: *p.Info.TotalTime, TransferMethod: "max(0, transit subPath count - 1); lane alternatives are not transfers", ProviderBusCount: p.Info.BusCount, ProviderSubwayCount: p.Info.SubwayCount, Legs: []Leg{}}
		transit := 0
		for index, leg := range p.Legs {
			l := Leg{Index: index, DurationSeconds: leg.Seconds, ProviderData: leg, DurationMinutes: *leg.Minutes}
			switch leg.TrafficType {
			case 1:
				l.Kind = "subway"
			case 2:
				l.Kind = "bus"
			case 3:
				l.Kind = "walk"
			}
			if leg.StartID != "" || leg.StartName != "" {
				l.Boarding = &JourneyStop{ODsayID: leg.StartID, LocalID: leg.StartLocal, Provider: leg.StartProvider, Name: leg.StartName, ARS: leg.StartARS, Lon: leg.StartX, Lat: leg.StartY}
			}
			if leg.EndID != "" || leg.EndName != "" {
				l.Alighting = &JourneyStop{ODsayID: leg.EndID, LocalID: leg.EndLocal, Provider: leg.EndProvider, Name: leg.EndName, ARS: leg.EndARS, Lon: leg.EndX, Lat: leg.EndY}
			}
			if leg.TrafficType == 1 || leg.TrafficType == 2 {
				transit++
			}
			if leg.TrafficType == 2 {
				for _, lane := range leg.Lanes {
					option := BusOption{Lane: lane, M5121Candidate: lane.BusNumber == "M5121", CongestionStatus: "not_provided", CongestionReason: "no_stored_reference_for_route"}
					if lane.Namespace == "kakao" {
						option.CongestionStatus = "unknown"
						option.CongestionReason = "provider_station_ids_unavailable"
						option.Mapping = &BridgeResult{Status: "unknown", BoardingStatus: "provider_station_ids_unavailable", AlightingStatus: "provider_station_ids_unavailable", Notes: []string{"kakao_names_are_not_official_GBIS_identifiers"}}
					}
					if option.M5121Candidate {
						c.ContainsM5121 = true
					}
					if s.Reader != nil && lane.Namespace != "kakao" && (option.M5121Candidate || lane.Type != nil && (*lane.Type == 4 || *lane.Type == 14)) {
						m, e := s.Reader.Resolve(ctx, q, lane, leg)
						if e != nil {
							return out, e
						}
						option.Mapping = &m
						option.CongestionReason = m.Status
						var ref struct {
							Status string `json:"status"`
						}
						if len(m.Reference) > 0 && json.Unmarshal(m.Reference, &ref) == nil && ref.Status == "available" {
							option.CongestionStatus = "available"
							option.CongestionReason = "historical_observations_only"
						}
					}
					l.Options = append(l.Options, option)
				}
			}
			c.Legs = append(c.Legs, l)
		}
		c.TransferCount = max(0, transit-1)
		out.Paths = append(out.Paths, c)
	}
	if s.Selections != nil {
		captured := time.Now()
		if out.Source != nil {
			captured = out.Source.CapturedAt
		} else if timed, ok := s.Searcher.(interface{ ReceivedAt(Query) time.Time }); ok {
			captured = timed.ReceivedAt(q)
		}
		id, expires, err := s.Selections.Save(raw, q, origin, captured)
		if err != nil {
			return out, ErrState
		}
		out.SearchID = id
		out.SearchExpiresAt = &expires
	}
	EvaluateCandidates(&out)
	if s.Boarding != nil {
		s.enrichBoarding(ctx, q, &out)
	}
	if q.EvaluationModel == "boarding_aware_v1" {
		EvaluateBoarding(&out, *s.BoardingWeights)
	}
	return out, nil
}

// Enrichment never changes provider order, ranks, or the v1 evaluation.
func (s *Service) enrichBoarding(ctx context.Context, q Query, out *Candidates) {
	departure, _ := time.Parse(time.RFC3339, q.DepartureAt)
	out.Policy["boarding_context"] = "independent_history_and_current_seats_v1"
	for pi := range out.Paths {
		elapsed := time.Duration(0)
		for li := range out.Paths[pi].Legs {
			leg := &out.Paths[pi].Legs[li]
			for oi := range leg.Options {
				option := &leg.Options[oi]
				m := option.Mapping
				if option.Mapping == nil {
					continue
				}
				a := boarding.Analysis{Status: "mapping_unverified", History: boarding.History{Status: "unavailable", Groups: []boarding.Profile{}}, Realtime: boarding.Realtime{Status: "unavailable", Vehicles: []boarding.SeatVehicle{}}}
				if m != nil && m.BoardingStatus == "matched_declared_gbis_local_id" && m.AlightingStatus == "matched_declared_gbis_local_id" && m.Status != "visit_order_conflict" && m.RouteID != nil && m.BoardingVisitID != nil && leg.Boarding != nil {
					at := time.Time{}
					if !departure.IsZero() {
						at = departure.Add(elapsed)
					}
					a = s.Boarding.Analyze(ctx, boarding.Request{RouteID: *m.RouteID, VisitID: *m.BoardingVisitID, GBISRoute: option.Lane.LocalID, GBISStop: leg.Boarding.LocalID, BoardingAt: at, ServiceDate: q.BoardingServiceDate})
				}
				option.BoardingAnalysis = &a
			}
			if leg.DurationSeconds != nil {
				elapsed += time.Duration(*leg.DurationSeconds) * time.Second
			} else {
				elapsed += time.Duration(leg.DurationMinutes * float64(time.Minute))
			}
		}
	}
}

// PathAllowed is shared by rendering and later index-based selection.
func PathAllowed(mode string, kind int) bool {
	switch mode {
	case "all":
		return true
	case "mixed":
		return kind == 3
	case "bus":
		return kind == 2
	case "subway":
		return kind == 1
	case "walk":
		return kind == 4
	}
	return false
}
