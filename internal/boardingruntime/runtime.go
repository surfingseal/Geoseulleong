// Package boardingruntime composes opt-in boarding features without startup API requests.
package boardingruntime

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/boarding"
	"geoseulleong/internal/gbis"
	"geoseulleong/internal/repository"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ReserveCall                      func(context.Context) error
	PersistentDaily, PersistentTotal int
	HistoryEnabled                   bool
	RealtimeEnabled                  bool
	MaxCalls                         int
	Dataset                          repository.BoardingConfig
	key                              string
}

func (Config) String() string   { return "boarding runtime configuration (credentials omitted)" }
func (Config) GoString() string { return "boarding runtime configuration (credentials omitted)" }
func flag(get func(string) string, name string) (bool, error) {
	switch get(name) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New(name + " must be true or false")
	}
}

// Existing BOARDING_CONTEXT_ENABLED retains its history-enabling meaning.
// Both flags default false. Key presence alone can never activate a provider.
func ReadConfig(get func(string) string) (Config, error) {
	c := Config{MaxCalls: 1}
	var e error
	c.HistoryEnabled, e = flag(get, "BOARDING_CONTEXT_ENABLED")
	if e != nil {
		return c, e
	}
	c.RealtimeEnabled, e = flag(get, "BOARDING_REALTIME_ENABLED")
	if e != nil {
		return c, e
	}
	if !c.HistoryEnabled && !c.RealtimeEnabled {
		return c, nil
	}
	if json.Unmarshal([]byte(get("BOARDING_DATASET_JSON")), &c.Dataset) != nil {
		return Config{}, errors.New("BOARDING_DATASET_JSON invalid")
	}
	if c.RealtimeEnabled {
		c.key = strings.TrimSpace(get("GBIS_API_KEY"))
		if c.key == "" {
			return Config{}, errors.New("BOARDING_REALTIME_ENABLED requires GBIS_API_KEY")
		}
		if s := get("BOARDING_GBIS_MAX_CALLS"); s != "" {
			n, e := strconv.Atoi(s)
			if e != nil || n < 1 || n > 3 {
				return Config{}, errors.New("BOARDING_GBIS_MAX_CALLS must be 1..3")
			}
			c.MaxCalls = n
		}
	}
	return c, nil
}

// Build is lazy with respect to GBIS: client construction never fetches.
// The per-process lifetime budget cannot provide a multi-instance global quota.
func Build(ctx context.Context, db repository.DB, c Config) (*boarding.Service, error) {
	if !c.HistoryEnabled && !c.RealtimeEnabled {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("boarding repository connection required")
	}
	resolver, e := repository.NewBoarding(db, c.Dataset)
	if e != nil {
		return nil, errors.New("boarding dataset configuration invalid")
	}
	if c.HistoryEnabled {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		rows, e := db.Query(bounded, `SELECT DISTINCT b.internal_route_id FROM congestion_batches b JOIN congestion_analysis_regions r USING(batch_id) WHERE r.analysis_set_id=ANY($1::text[]) AND r.service_date::text=ANY($2::text[]) AND b.internal_route_id IS NOT NULL`, c.Dataset.SetIDs, c.Dataset.Dates)
		if e != nil {
			return nil, errors.New("boarding dataset unavailable")
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) != nil {
				rows.Close()
				return nil, errors.New("boarding route inventory invalid")
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil || len(ids) == 0 || len(ids) > 16 {
			return nil, errors.New("boarding dataset route inventory unavailable")
		}
		for _, id := range ids {
			if resolver.ValidateDataset(bounded, id) != nil {
				return nil, errors.New("boarding dataset does not match selected dates and sets")
			}
		}
	}
	service := &boarding.Service{Resolver: resolver}
	if c.HistoryEnabled {
		service.History = resolver
	}
	if c.RealtimeEnabled {
		var client *gbis.Client
		var e error
		if c.ReserveCall != nil {
			client, _, e = gbis.NewPersistentRealtimeClient(c.key, true, gbis.ValidationBudgetConfig{DailyLimit: c.PersistentDaily, TotalLimit: c.PersistentTotal, ReserveCall: c.ReserveCall})
		} else {
			client, e = gbis.NewLimited(c.key, c.MaxCalls)
		}
		if e != nil {
			return nil, errors.New("GBIS client configuration invalid")
		}
		service.Realtime = boarding.NewSeatCache(timedArrival{client: client})
	}
	return service, nil
}

type timedArrival struct{ client *gbis.Client }

func (p timedArrival) Arrival(ctx context.Context, route string, stop gbis.Station) (gbis.Arrival, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a, e := p.client.Arrival(bounded, route, stop)
	if bounded.Err() != nil {
		return a, bounded.Err()
	}
	return a, e
}
