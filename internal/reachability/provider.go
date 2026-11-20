package reachability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"geoseulleong/internal/gbis"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Provider interface {
	Locations(context.Context, string) (LocationsSnapshot, error)
	Arrival(context.Context, string, gbis.Station) (ArrivalSnapshot, error)
}

// GBISAdapter borrows an explicitly supplied, already-budgeted GBIS client.
// It never constructs a client, changes its limits, or retries a request.
type GBISClient interface {
	Locations(context.Context, string) (gbis.LocationResult, error)
	Arrival(context.Context, string, gbis.Station) (gbis.Arrival, error)
}
type GBISAdapter struct{ Client GBISClient }

func (p GBISAdapter) Locations(c context.Context, r string) (LocationsSnapshot, error) {
	if p.Client == nil {
		return LocationsSnapshot{}, errors.New("GBIS disabled")
	}
	v, e := p.Client.Locations(c, r)
	return LocationsSnapshot{Route: r, Locations: v, ReceivedAt: time.Now(), Origin: "live_gbis_api"}, e
}
func (p GBISAdapter) Arrival(c context.Context, r string, s gbis.Station) (ArrivalSnapshot, error) {
	if p.Client == nil {
		return ArrivalSnapshot{}, errors.New("GBIS disabled")
	}
	v, e := p.Client.Arrival(c, r, s)
	return ArrivalSnapshot{Arrival: v, ReceivedAt: time.Now(), Origin: "live_gbis_api"}, e
}

type manifest struct {
	Route         string    `json:"route_id"`
	Origin        string    `json:"data_origin"`
	LocationsSHA  string    `json:"locations_sha256"`
	LocationsFile string    `json:"locations_xml"`
	LocationsAt   time.Time `json:"locations_received_at"`
	Arrivals      []struct {
		Station gbis.Station `json:"station"`
		SHA     string       `json:"sha256"`
		File    string       `json:"xml"`
		At      time.Time    `json:"received_at"`
	} `json:"arrivals"`
}
type Replay struct {
	l LocationsSnapshot
	a []ArrivalSnapshot
}

func bounded(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("GBIS replay file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 2<<20+1))
	if e != nil || len(b) > 2<<20 {
		return nil, errors.New("GBIS replay file invalid")
	}
	return b, nil
}
func LoadReplay(path string) (*Replay, error) {
	b, e := bounded(path)
	if e != nil {
		return nil, e
	}
	var m manifest
	if json.Unmarshal(b, &m) != nil || m.LocationsAt.IsZero() || len(m.Arrivals) > 4 || (m.Origin != "synthetic_fixture" && m.Origin != "gbis_recorded_response") {
		return nil, errors.New("invalid GBIS manifest")
	}
	raw, e := bounded(filepath.Join(filepath.Dir(path), m.LocationsFile))
	if e != nil {
		return nil, e
	}
	if m.Origin == "gbis_recorded_response" && !checksum(raw, m.LocationsSHA) {
		return nil, errors.New("recorded locations checksum mismatch")
	}
	loc, e := gbis.ParseLocationsXML(raw, 200, m.Route)
	if e != nil {
		return nil, e
	}
	p := &Replay{l: LocationsSnapshot{Route: m.Route, Locations: loc, ReceivedAt: m.LocationsAt, Origin: m.Origin}}
	seen := map[string]bool{}
	for _, entry := range m.Arrivals {
		if entry.At.IsZero() {
			return nil, errors.New("missing receive time")
		}
		raw, e = bounded(filepath.Join(filepath.Dir(path), entry.File))
		if e != nil {
			return nil, e
		}
		if m.Origin == "gbis_recorded_response" && !checksum(raw, entry.SHA) {
			return nil, errors.New("recorded arrival checksum mismatch")
		}
		a, e := gbis.ParseArrivalXML(raw, 200, m.Route, entry.Station)
		if e != nil {
			return nil, e
		}
		key := a.StationID + "/" + strconv.Itoa(a.Order)
		if seen[key] {
			return nil, errors.New("duplicate stop replay")
		}
		seen[key] = true
		p.a = append(p.a, ArrivalSnapshot{Arrival: a, ReceivedAt: entry.At, Origin: m.Origin})
	}
	return p, nil
}
func (p *Replay) Locations(ctx context.Context, route string) (LocationsSnapshot, error) {
	if ctx.Err() != nil {
		return LocationsSnapshot{}, ctx.Err()
	}
	if route != p.l.Route {
		return LocationsSnapshot{}, errors.New("route mismatch")
	}
	return p.l, nil
}
func (p *Replay) Arrival(ctx context.Context, route string, s gbis.Station) (ArrivalSnapshot, error) {
	if ctx.Err() != nil {
		return ArrivalSnapshot{}, ctx.Err()
	}
	for _, a := range p.a {
		if a.Arrival.RouteID == route && a.Arrival.StationID == s.ID && a.Arrival.Order == s.Order {
			return a, nil
		}
	}
	return ArrivalSnapshot{}, errors.New("stop snapshot missing")
}

func checksum(raw []byte, expected string) bool {
	h := sha256.Sum256(raw)
	return expected != "" && hex.EncodeToString(h[:]) == expected
}
