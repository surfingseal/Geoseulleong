package kakao

import (
	"encoding/json"
	"geoseulleong/internal/odsay"
	"math"
)

func number(v *int) bool     { return v != nil && *v >= 0 && *v <= 90000000 }
func minute(v *int) *float64 { x := float64(*v) / 60; return &x }
func count(v int) *int       { return &v }
func geometry(g Geometry) bool {
	for _, p := range g.Points {
		if len(p) != 2 || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) || p[0] < -180 || p[0] > 180 || p[1] < -90 || p[1] > 90 {
			return false
		}
	}
	return true
}
func transitRoute(r TransitRoute) (odsay.Path, error) {
	p := odsay.Path{}
	switch r.Properties.Type {
	case "BUS":
		p.Type = 2
	case "SUBWAY":
		p.Type = 1
	case "BUS_AND_SUBWAY":
		p.Type = 3
	default:
		return p, odsay.ErrSchema
	}
	if !number(r.Properties.Time) || !number(r.Properties.Transfers) || !number(r.Properties.Distance) || len(r.Steps) == 0 || len(r.Steps) > 100 {
		return p, odsay.ErrSchema
	}
	p.Info.TotalTime = minute(r.Properties.Time)
	p.Info.Seconds = r.Properties.Time
	p.Info.Transfers = r.Properties.Transfers
	buses, trains := 0, 0
	for _, step := range r.Steps {
		s := step.Properties
		if !number(s.Time) || !number(s.Distance) || !geometry(step.Path) {
			return p, odsay.ErrSchema
		}
		leg := odsay.SubPath{Namespace: "kakao", Seconds: s.Time, Minutes: minute(s.Time)}
		switch s.Type {
		case "BUS":
			leg.TrafficType = 2
			buses++
		case "SUBWAY":
			leg.TrafficType = 1
			trains++
		case "WALKING":
			leg.TrafficType = 3
		default:
			return p, odsay.ErrSchema
		}
		if len(s.Stops) > 0 {
			leg.StartName = s.Stops[0].Name
			leg.EndName = s.Stops[len(s.Stops)-1].Name
		}
		for _, v := range s.Vehicles {
			lane := odsay.Lane{Namespace: "kakao", Kind: v.Type}
			if s.Type == "BUS" {
				lane.BusNumber = v.Name
			} else {
				lane.Name = v.Name
			}
			leg.Lanes = append(leg.Lanes, lane)
		}
		// Preserve all provided path points/stops/options without manufacturing local IDs.
		leg.Details, _ = json.Marshal(step)
		p.Legs = append(p.Legs, leg)
	}
	if p.Type == 2 && (buses == 0 || trains != 0) || p.Type == 1 && (trains == 0 || buses != 0) || p.Type == 3 && (buses == 0 || trains == 0) {
		return p, odsay.ErrSchema
	}
	return p, nil
}
func ParseTransit(raw []byte) (odsay.Response, error) {
	out := odsay.Response{Paths: []odsay.Path{}}
	var r TransitResponse
	if json.Unmarshal(raw, &r) != nil {
		return out, odsay.ErrSchema
	}
	if r.Status == "NO_RESULTS" {
		return out, nil
	}
	if r.Status != "OK" {
		switch r.Status {
		case "STARTNODES_NULL", "ENDNODES_NULL", "EQUAL_POINTS", "INVALID_REQUEST":
			return out, odsay.ErrInput
		}
		return out, odsay.ErrSchema
	}
	if r.Properties.Total == nil || *r.Properties.Total != len(r.Routes) || len(r.Routes) > 50 {
		return out, odsay.ErrSchema
	}
	counts := map[int]int{}
	for _, route := range r.Routes {
		p, e := transitRoute(route)
		if e != nil {
			return out, e
		}
		counts[p.Type]++
		out.Paths = append(out.Paths, p)
	}
	for _, c := range []struct {
		v    *int
		kind int
	}{{r.Properties.Bus, 2}, {r.Properties.Subway, 1}, {r.Properties.Mixed, 3}} {
		if c.v == nil || *c.v != counts[c.kind] {
			return out, odsay.ErrSchema
		}
	}
	return out, nil
}
func ParseWalking(raw []byte) (odsay.Response, error) {
	out := odsay.Response{Paths: []odsay.Path{}}
	var r WalkingResponse
	if json.Unmarshal(raw, &r) != nil {
		return out, odsay.ErrSchema
	}
	switch r.Status {
	case "ROUTE_RESULT_NOT_FOUND":
		return out, nil
	case "SAME_POINT", "START_LINK_NOT_FOUND", "END_LINK_NOT_FOUND", "TOO_FAR_AWAY", "TOO_MANY_SEARCH_LINK":
		return out, odsay.ErrInput
	case "OK":
	default:
		return out, odsay.ErrSchema
	}
	if r.Route == nil || !number(r.Route.Properties.Time) || !number(r.Route.Properties.Distance) || len(r.Route.Legs) == 0 || len(r.Route.Legs) > 100 {
		return out, odsay.ErrSchema
	}
	p := odsay.Path{Type: 4}
	p.Info.TotalTime = minute(r.Route.Properties.Time)
	p.Info.Seconds = r.Route.Properties.Time
	for _, leg := range r.Route.Legs {
		if !number(leg.Properties.Time) || !number(leg.Properties.Distance) || len(leg.Steps) == 0 || len(leg.Steps) > 1000 {
			return out, odsay.ErrSchema
		}
		for _, s := range leg.Steps {
			if !number(s.Properties.Time) || !number(s.Properties.Distance) || !geometry(s.Path) {
				return out, odsay.ErrSchema
			}
			if (s.Properties.X == nil) != (s.Properties.Y == nil) {
				return out, odsay.ErrSchema
			}
			if s.Properties.X != nil && !geometry(Geometry{Points: [][]float64{{*s.Properties.X, *s.Properties.Y}}}) {
				return out, odsay.ErrSchema
			}
		}
		details, _ := json.Marshal(leg)
		p.Legs = append(p.Legs, odsay.SubPath{TrafficType: 3, Namespace: "kakao", Seconds: leg.Properties.Time, Minutes: minute(leg.Properties.Time), Details: details})
	}
	out.Paths = append(out.Paths, p)
	return out, nil
}
