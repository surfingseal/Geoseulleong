// Package collectionrunner executes a closed, verified manifest. No background work is registered.
package collectionrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"geoseulleong/internal/collectionpreview"
	"geoseulleong/internal/stcismetadata"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"strings"
)

type Document struct {
	URL string `json:"source_url"`
	Raw []byte `json:"raw_response_base64"`
	SHA string `json:"sha256"`
}
type Identity struct {
	STCIS      string   `json:"stcis_route_id"`
	GBIS       string   `json:"gbis_route_id"`
	Internal   int64    `json:"internal_route_id"`
	Name       string   `json:"route_name"`
	GBISRoute  Document `json:"gbis_route"`
	GBISVisits Document `json:"gbis_visits"`
	STCISRoute Document `json:"stcis_route"`
	STCISStops Document `json:"stcis_stops"`
}

func Digest(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }
func (d Document) valid(host, endpoint string) bool {
	u, e := url.Parse(d.URL)
	return e == nil && u.Scheme == "https" && u.Hostname() == host && u.Path == endpoint && u.RawQuery == "" && u.User == nil && len(d.Raw) > 0 && Digest(d.Raw) == d.SHA
}

// Verify checks provider IDs and at least two unambiguous BIS stops in matching order,
// not merely matching route names. Evidence hash must be separately reviewed for live use.
func (i Identity) Verify(r collectionpreview.Route) error {
	if i.STCIS != r.STCIS || i.GBIS != r.GBIS || i.Internal != r.Internal || i.Name != r.Name || i.Internal < 1 {
		return errors.New("route identity binding mismatch")
	}
	if !i.GBISRoute.valid("apis.data.go.kr", "/6410000/busrouteservice/v2/getBusRouteListv2") || !i.GBISVisits.valid("apis.data.go.kr", "/6410000/busrouteservice/v2/getBusRouteStationListv2") || !i.STCISRoute.valid("stcis.go.kr", "/openapi/busroutesttn.json") || !i.STCISStops.valid("stcis.go.kr", "/openapi/bussttn.json") {
		return errors.New("official evidence origin/hash missing")
	}
	var route struct {
		Response struct {
			Header struct {
				Code json.Number `json:"resultCode"`
			} `json:"msgHeader"`
			Body struct {
				Rows json.RawMessage `json:"busRouteList"`
			} `json:"msgBody"`
		} `json:"response"`
	}
	if json.Unmarshal(i.GBISRoute.Raw, &route) != nil || route.Response.Header.Code.String() != "0" {
		return errors.New("GBIS route response invalid")
	}
	var rows []map[string]json.RawMessage
	if json.Unmarshal(route.Response.Body.Rows, &rows) != nil {
		var row map[string]json.RawMessage
		if json.Unmarshal(route.Response.Body.Rows, &row) != nil {
			return errors.New("GBIS route rows invalid")
		}
		rows = append(rows, row)
	}
	matches := 0
	for _, r := range rows {
		if scalar(r["routeId"]) == i.GBIS {
			if scalar(r["routeName"]) != i.Name {
				return errors.New("GBIS name conflict")
			}
			matches++
		}
	}
	if matches != 1 {
		return errors.New("GBIS route ambiguous")
	}
	var visits struct {
		Response struct {
			Header struct {
				Code json.Number `json:"resultCode"`
			} `json:"msgHeader"`
			Body struct {
				Rows []struct {
					ID  json.Number `json:"stationId"`
					Seq int         `json:"stationSeq"`
				} `json:"busRouteStationList"`
			} `json:"msgBody"`
		} `json:"response"`
	}
	if json.Unmarshal(i.GBISVisits.Raw, &visits) != nil || visits.Response.Header.Code.String() != "0" {
		return errors.New("GBIS visit evidence invalid")
	}
	seq := map[string]int{}
	last := 0
	for _, v := range visits.Response.Body.Rows {
		if v.Seq <= last {
			return errors.New("GBIS visit order invalid")
		}
		last = v.Seq
		id := v.ID.String()
		if _, ok := seq[id]; ok {
			seq[id] = -1
		} else {
			seq[id] = v.Seq
		}
	}
	// ParseRouteStops additionally enforces official status/count, route and province.
	var env struct {
		Rows []struct {
			Province string `json:"sdCd"`
		} `json:"result"`
	}
	if json.Unmarshal(i.STCISRoute.Raw, &env) != nil || len(env.Rows) == 0 {
		return errors.New("STCIS route missing")
	}
	sr, _, e := stcismetadata.ParseRouteStops(metadataEnvelope(i.STCISRoute.Raw), i.STCIS, env.Rows[0].Province)
	if e != nil {
		return e
	}
	stops, _, e := stcismetadata.ParseStops(metadataEnvelope(i.STCISStops.Raw))
	if e != nil {
		return e
	}
	bis := map[string]string{}
	for _, s := range stops {
		if old, ok := bis[s.ID]; ok && old != s.BISID {
			return errors.New("conflicting BIS stop")
		}
		bis[s.ID] = s.BISID
	}
	links := 0
	lastG, lastS := 0, -1
	seen := map[string]bool{}
	for _, s := range sr {
		if s.RouteNumber != i.Name || s.Sequence <= lastS {
			return errors.New("STCIS route/order conflict")
		}
		lastS = s.Sequence
		id := bis[s.ID]
		g := seq[id]
		if id == "" || g <= 0 {
			continue
		}
		if seen[id] || g <= lastG {
			return errors.New("ambiguous/reversed BIS membership")
		}
		seen[id] = true
		lastG = g
		links++
	}
	if links < 2 {
		return errors.New("insufficient official BIS membership crosswalk")
	}
	return nil
}
func scalar(b []byte) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(b))
}

// VerifyDB performs SELECT only and never writes mapping evidence or historical validity.
func (i Identity) VerifyDB(ctx context.Context, p *pgxpool.Pool) error {
	var n int
	e := p.QueryRow(ctx, `SELECT count(*) FROM bus_routes b JOIN route_external_ids x USING(route_id) WHERE b.route_id=$1 AND b.route_number=$2 AND x.source_id='gbis' AND x.external_route_id=$3 AND x.verified_at IS NOT NULL`, i.Internal, i.Name, i.GBIS).Scan(&n)
	if e != nil || n != 1 {
		return errors.New("internal/GBIS route mapping not verified")
	}
	return nil
}

// Recorded official metadata envelopes omit status. Count/rows validation remains
// in the shared parser; only its optional legacy status sentinel is adapted.
// Original bytes and their digest are never rewritten.
func metadataEnvelope(raw []byte) []byte {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	if _, ok := v["status"]; !ok {
		v["status"] = json.RawMessage(`"OK"`)
	}
	b, _ := json.Marshal(v)
	return b
}
