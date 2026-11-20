package boarding

import (
	"context"
	"errors"
	"geoseulleong/internal/gbis"
	"strconv"
	"sync"
	"time"
)

type ArrivalProvider interface {
	Arrival(context.Context, string, gbis.Station) (gbis.Arrival, error)
}
type ArrivalSnapshot struct {
	Arrival    gbis.Arrival
	ReceivedAt time.Time
	Origin     string
}
type cacheEntry struct {
	snapshot    ArrivalSnapshot
	err         bool
	errorStatus string
	fetched     time.Time
}

// One serialized cache bounds duplicate/concurrent fetches. No background refresh/retry.
// GBIS's existing Client additionally enforces >=2s and lifetime request budget.
type SeatCache struct {
	mu       sync.Mutex
	provider ArrivalProvider
	entries  map[string]cacheEntry
	now      func() time.Time
}

func NewSeatCache(p ArrivalProvider) *SeatCache {
	return &SeatCache{provider: p, entries: map[string]cacheEntry{}, now: time.Now}
}
func (c *SeatCache) Seats(ctx context.Context, s Stop) Realtime {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil {
		return Realtime{Status: "disabled", Vehicles: []SeatVehicle{}}
	}
	key := s.GBISRoute + "/" + s.GBISStop + "/" + strconv.Itoa(s.Sequence)
	now := c.now()
	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.fetched) >= 30*time.Second {
		a, e := c.provider.Arrival(ctx, s.GBISRoute, gbis.Station{ID: s.GBISStop, Order: s.Sequence})
		entry = cacheEntry{snapshot: ArrivalSnapshot{Arrival: a, ReceivedAt: c.now(), Origin: "live_gbis_api"}, err: e != nil, errorStatus: seatErrorStatus(e), fetched: c.now()}
		if len(c.entries) >= 128 {
			clear(c.entries)
		}
		c.entries[key] = entry
	}
	if entry.err {
		return Realtime{Status: entry.errorStatus, Vehicles: []SeatVehicle{}}
	}
	return ValidateArrival(s, entry.snapshot, c.now())
}
func ValidateArrival(s Stop, snapshot ArrivalSnapshot, now time.Time) Realtime {
	a := snapshot.Arrival
	out := Realtime{Status: "unavailable", Origin: snapshot.Origin, ReceivedAt: &snapshot.ReceivedAt, Vehicles: []SeatVehicle{}, Meaning: "current_reported_vehicle_seats_not_future_boarding_guarantee"}
	if a.RouteID != s.GBISRoute || a.StationID != s.GBISStop || a.Order != s.Sequence {
		out.Status = "identity_mismatch"
		return out
	}
	if snapshot.ReceivedAt.IsZero() || now.Sub(snapshot.ReceivedAt) > 60*time.Second || snapshot.ReceivedAt.After(now.Add(5*time.Second)) {
		out.Status = "stale"
	}
	seen := map[string]bool{}
	for i, v := range a.Vehicles {
		item := SeatVehicle{ArrivalIndex: i + 1, VehicleID: v.VehicleID, PlateNo: v.PlateNo, ETA: v.Minutes, ETASeconds: v.Seconds, Status: "unavailable"}
		if v.VehicleID != nil && *v.VehicleID != "" {
			if seen[*v.VehicleID] {
				out.Status = "vehicle_identity_conflict"
				out.Vehicles = []SeatVehicle{}
				return out
			}
			seen[*v.VehicleID] = true
			if v.Seats != nil && *v.Seats >= 0 && (v.Minutes != nil && *v.Minutes >= 0 || v.Seconds != nil && *v.Seconds >= 0) {
				item.Seats = v.Seats
				item.Status = "available"
			}
		}
		if out.Status == "stale" {
			item.Status = "stale"
			item.Seats = nil
		}
		out.Vehicles = append(out.Vehicles, item)
	}
	if out.Status != "stale" {
		for _, v := range out.Vehicles {
			if v.Status == "available" {
				out.Status = "available"
			}
		}
	}
	return out
}

func seatErrorStatus(e error) string {
	if e == nil {
		return ""
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(e, context.Canceled) {
		return "cancelled"
	}
	var g *gbis.Error
	if errors.As(e, &g) {
		switch g.Kind {
		case "arrival_identity_mismatch":
			return "identity_mismatch"
		case "request_limit":
			return "budget_exhausted"
		case "gateway_auth_or_service":
			return "authentication_or_service_error"
		case "no_data":
			return "unavailable"
		}
		if g.Kind == "api" && g.APICode == "4" {
			return "unavailable"
		}
	}
	return "api_error"
}
