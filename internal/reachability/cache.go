package reachability

import (
	"context"
	"geoseulleong/internal/gbis"
	"strconv"
	"sync"
	"time"
)

// Cached serializes requests and reuses the received timestamp for 30 seconds.
// Errors are cached too. The supplied client's durable budget remains authoritative.
type Cached struct {
	mu        sync.Mutex
	source    Provider
	now       func() time.Time
	locations map[string]cachedLocation
	arrivals  map[string]cachedArrival
}
type cachedLocation struct {
	value LocationsSnapshot
	err   error
	at    time.Time
}
type cachedArrival struct {
	value ArrivalSnapshot
	err   error
	at    time.Time
}

func NewCached(p Provider) *Cached {
	return &Cached{source: p, now: time.Now, locations: map[string]cachedLocation{}, arrivals: map[string]cachedArrival{}}
}
func (c *Cached) Locations(ctx context.Context, route string) (LocationsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return LocationsSnapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	v, ok := c.locations[route]
	if ok && now.Sub(v.at) < 30*time.Second {
		return v.value, v.err
	}
	x, e := c.source.Locations(ctx, route)
	if len(c.locations) >= 128 {
		for k, v := range c.locations {
			if now.Sub(v.at) >= 30*time.Second {
				delete(c.locations, k)
			}
		}
	}
	if len(c.locations) < 128 {
		c.locations[route] = cachedLocation{x, e, c.now()}
	}
	return x, e
}
func (c *Cached) Arrival(ctx context.Context, route string, stop gbis.Station) (ArrivalSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return ArrivalSnapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	key := route + "/" + stop.ID + "/" + strconv.Itoa(stop.Order)
	v, ok := c.arrivals[key]
	if ok && now.Sub(v.at) < 30*time.Second {
		return v.value, v.err
	}
	x, e := c.source.Arrival(ctx, route, stop)
	if len(c.arrivals) >= 128 {
		for k, v := range c.arrivals {
			if now.Sub(v.at) >= 30*time.Second {
				delete(c.arrivals, k)
			}
		}
	}
	if len(c.arrivals) < 128 {
		c.arrivals[key] = cachedArrival{x, e, c.now()}
	}
	return x, e
}
