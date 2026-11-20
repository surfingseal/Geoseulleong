// Package collectionpreview plans work offline; it cannot fetch, ingest, or approve a scope.
package collectionpreview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/routecongestion"
	"math"
	"regexp"
	"sort"
	"time"
)

type Region struct {
	Province string `json:"ctpv_cd"`
	District string `json:"sgg_cd"`
}
type Route struct {
	STCIS         string   `json:"stcis_route_id"`
	GBIS          string   `json:"gbis_route_id"`
	Internal      int64    `json:"internal_route_id"`
	Name          string   `json:"route_name"`
	MappingStatus string   `json:"mapping_status"`
	EvidenceSHA   string   `json:"mapping_evidence_sha256"`
	Regions       []Region `json:"regions"`
}
type Budget struct {
	MaxRequests int64 `json:"proposed_max_requests"`
	MaxPages    int64 `json:"max_pages_per_scope"`
	Interval    int   `json:"min_interval_seconds"`
	Concurrency int   `json:"concurrency"`
}
type StorageModel struct {
	RawBytesPerRow   float64 `json:"raw_bytes_per_row"`
	ResponseOverhead int64   `json:"response_overhead_bytes"`
	ArchiveCopies    float64 `json:"archive_copy_factor"`
	DBLow            int64   `json:"db_bytes_per_observation_low"`
	DBHigh           int64   `json:"db_bytes_per_observation_high"`
	DBQueryLow       int64   `json:"db_bytes_per_query_low"`
	DBQueryHigh      int64   `json:"db_bytes_per_query_high"`
	Basis            string  `json:"basis"`
}
type Forecast struct {
	Route  string `json:"stcis_route_id"`
	Region Region `json:"region"`
	Rows   int64  `json:"expected_rows"`
	Basis  string `json:"basis"`
}
type Manifest struct {
	Origin    string            `json:"data_origin"`
	Dates     []string          `json:"dates"`
	Routes    []Route           `json:"routes"`
	Budget    Budget            `json:"budget"`
	Model     StorageModel      `json:"storage_model"`
	Forecasts []Forecast        `json:"forecasts"`
	Chosen    map[string]string `json:"chosen_snapshot_by_scope"`
}
type SavedPage struct {
	Number int64  `json:"page"`
	Rows   int64  `json:"record_count"`
	SHA    string `json:"response_sha256"`
}
type Snapshot struct {
	Scope    routecongestion.Scope `json:"scope"`
	ID       string                `json:"snapshot_or_checkpoint_id"`
	State    string                `json:"state"` // complete / no_data_for_query / partial / pending / ambiguous / failed
	Total    *int64                `json:"reported_total_count"`
	Pages    []SavedPage           `json:"validated_pages"`
	Ingested bool                  `json:"already_ingested"`
}
type StorageEstimate struct {
	Raw     int64 `json:"new_raw_payload_bytes"`
	Archive int64 `json:"new_archive_bytes_estimate"`
	DBLow   int64 `json:"new_db_bytes_low_estimate"`
	DBHigh  int64 `json:"new_db_bytes_high_estimate"`
}
type Task struct {
	Scope            routecongestion.Scope `json:"scope"`
	Key              string                `json:"scope_key"`
	State            string                `json:"preview_state"`
	Snapshot         string                `json:"selected_snapshot_id,omitempty"`
	Mapping          string                `json:"route_mapping_status"`
	LegacyAllowed    bool                  `json:"legacy_scope_allowlist_matches"`
	Minimum          int64                 `json:"minimum_new_requests"`
	Exact            *int64                `json:"exact_new_requests"`
	Expected         *int64                `json:"expected_new_requests"`
	ExpectedRows     *int64                `json:"expected_new_db_rows"`
	ReusedRows       int64                 `json:"reused_existing_rows"`
	EstimateBasis    string                `json:"estimate_basis"`
	Storage          *StorageEstimate      `json:"storage_estimate"`
	Blockers         []string              `json:"blockers"`
	remainingRawRows *int64
	dbQueries        int64
}
type Preview struct {
	ID                      string            `json:"preview_id_not_an_analysis_set_id"`
	Origin                  string            `json:"data_origin"`
	Tasks                   []Task            `json:"tasks"`
	Minimum                 int64             `json:"minimum_new_requests"`
	Exact                   *int64            `json:"exact_new_requests"`
	Expected                *int64            `json:"expected_new_requests"`
	ExpectedRows            *int64            `json:"expected_new_db_rows"`
	ReusedRows              int64             `json:"reused_rows"`
	Storage                 *StorageEstimate  `json:"storage_estimate"`
	StopCap                 int64             `json:"global_request_stop_cap"`
	FitsExpected            *bool             `json:"expected_requests_fit_proposed_cap"`
	KnownPageCapViolations  int               `json:"known_scope_page_cap_violations"`
	BudgetAssuresCompletion bool              `json:"exact_request_budget_sufficient"`
	BlockedScopes           int               `json:"blocked_scopes"`
	ExecutionEnabled        bool              `json:"execution_enabled"`
	ActualCalls             int               `json:"actual_api_calls"`
	ByDate                  map[string]Rollup `json:"by_date"`
	ByRoute                 map[string]Rollup `json:"by_stcis_route"`
	ByRegion                map[string]Rollup `json:"by_region"`
	Policies                map[string]string `json:"policies"`
}
type Rollup struct {
	Scopes   int    `json:"scopes"`
	Minimum  int64  `json:"minimum_new_requests"`
	Expected *int64 `json:"expected_new_requests"`
	Rows     *int64 `json:"expected_new_db_rows"`
}

var numeric = regexp.MustCompile(`^[0-9]+$`)
var sha = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ScopeKey(s routecongestion.Scope) string {
	return s.Date + "/" + s.Route + "/" + s.Province + "/" + s.District
}
func regionKey(r string, g Region) string { return r + "/" + g.Province + "/" + g.District }
func ptr(v int64) *int64                  { return &v }
func pages(rows int64) int64 {
	if rows == 0 {
		return 1
	}
	return (rows + 999) / 1000
}
func sum(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	return ptr(*a + *b)
}
func estimate(rows, requests, dbRows, queries int64, m StorageModel) (*StorageEstimate, error) {
	values := []float64{float64(rows)*m.RawBytesPerRow + float64(requests)*float64(m.ResponseOverhead), float64(dbRows)*float64(m.DBLow) + float64(queries)*float64(m.DBQueryLow), float64(dbRows)*float64(m.DBHigh) + float64(queries)*float64(m.DBQueryHigh)}
	values = append(values, values[0]*m.ArchiveCopies)
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= float64(math.MaxInt64) {
			return nil, errors.New("storage estimate overflow")
		}
	}
	return &StorageEstimate{Raw: int64(math.Ceil(values[0])), Archive: int64(math.Ceil(values[3])), DBLow: int64(math.Ceil(values[1])), DBHigh: int64(math.Ceil(values[2]))}, nil
}
func validateSnapshot(s Snapshot) error {
	if !sha.MatchString(s.ID) {
		return errors.New("snapshot digest invalid")
	}
	if s.Total != nil && (*s.Total < 0 || *s.Total > 1000000000) {
		return errors.New("invalid total")
	}
	seen := map[int64]bool{}
	rows := int64(0)
	for _, p := range s.Pages {
		if p.Number < 1 || p.Rows < 0 || p.Rows > 1000 || !sha.MatchString(p.SHA) || seen[p.Number] {
			return errors.New("invalid or duplicated validated page")
		}
		seen[p.Number] = true
		rows += p.Rows
		if s.Total != nil {
			if p.Number > pages(*s.Total) {
				return errors.New("page outside reported total")
			}
			expected := int64(1000)
			if remain := *s.Total - (p.Number-1)*1000; remain < expected {
				expected = remain
			}
			if p.Rows != expected {
				return errors.New("page record count mismatch")
			}
		}
	}
	switch s.State {
	case "complete":
		if s.Total == nil || int64(len(s.Pages)) != pages(*s.Total) || rows != *s.Total {
			return errors.New("incomplete complete snapshot")
		}
	case "no_data_for_query":
		if s.Total != nil || len(s.Pages) != 0 {
			return errors.New("code50 must not masquerade as totalCount zero")
		}
	case "partial":
		if s.Ingested {
			return errors.New("partial checkpoint cannot be marked ingested")
		}
		if s.Total == nil || len(s.Pages) == 0 {
			return errors.New("partial requires validated first-page total")
		}
		if !seen[1] {
			return errors.New("missing first-page evidence")
		}
	case "pending", "ambiguous", "failed":
	default:
		return errors.New("unknown checkpoint state")
	}
	return nil
}

func Build(m Manifest, inventory []Snapshot) (Preview, error) {
	p := Preview{Origin: m.Origin, Tasks: []Task{}, Exact: ptr(0), Expected: ptr(0), ExpectedRows: ptr(0), StopCap: m.Budget.MaxRequests, ByDate: map[string]Rollup{}, ByRoute: map[string]Rollup{}, ByRegion: map[string]Rollup{}, Policies: map[string]string{"mode": "offline_preview_only_no_fetch_or_apply", "interpretation": "cgst/tzon/opr_trntm preserved; definitions unverified", "request_unit": "one date/STCIS-route/province/district/page; size1000", "selection": "one explicitly selected snapshot per scope; no raw-row dedup", "estimates": "forecast hypotheses, not API totalCount; code50 remains a separate query outcome", "storage": "uncompressed payload + declared archive/DB factors; excludes backups/WAL/replicas", "approval": "all new scopes and execution budgets require separate approval; legacy allowlist not expanded", "account_daily_quota": "unverified; proposed budget is not an account quota", "region_codes": "format validation only; official code evidence must be checked before collection"}}
	if m.Origin != "existing_and_proposed" && m.Origin != "synthetic_test" {
		return p, errors.New("explicit input provenance required")
	}
	if len(m.Dates) == 0 || len(m.Routes) == 0 || m.Budget.MaxRequests < 0 || m.Budget.MaxRequests > 1000000 || m.Budget.MaxPages < 1 || m.Budget.MaxPages > 10000 || m.Budget.Interval < 2 || m.Budget.Concurrency != 1 {
		return p, errors.New("invalid scope or proposed budget policy")
	}
	if m.Model.RawBytesPerRow <= 0 || math.IsNaN(m.Model.RawBytesPerRow) || math.IsInf(m.Model.RawBytesPerRow, 0) || m.Model.ResponseOverhead < 0 || m.Model.ArchiveCopies < 1 || m.Model.ArchiveCopies > 16 || m.Model.DBLow < 0 || m.Model.DBHigh < m.Model.DBLow || m.Model.DBQueryLow < 0 || m.Model.DBQueryHigh < m.Model.DBQueryLow || m.Model.Basis == "" {
		return p, errors.New("explicit storage assumptions required")
	}
	dates := map[string]bool{}
	for _, date := range m.Dates {
		d, e := time.Parse("20060102", date)
		if e != nil || d.Format("20060102") != date {
			return p, errors.New("invalid date")
		}
		dates[date] = true
	}
	forecasts := map[string]Forecast{}
	for _, f := range m.Forecasts {
		key := regionKey(f.Route, f.Region)
		if _, ok := forecasts[key]; ok || f.Rows < 0 || f.Rows > 1000000000 || f.Basis == "" {
			return p, errors.New("duplicate or invalid forecast")
		}
		forecasts[key] = f
	}
	stored := map[string]map[string]Snapshot{}
	snapshotScopes := map[string]string{}
	for _, s := range inventory {
		if e := validateSnapshot(s); e != nil {
			return p, e
		}
		key := ScopeKey(s.Scope)
		if old := snapshotScopes[s.ID]; old != "" && old != key {
			return p, errors.New("snapshot ID reused across different scopes")
		}
		snapshotScopes[s.ID] = key
		s.Pages = append([]SavedPage(nil), s.Pages...)
		sort.Slice(s.Pages, func(i, j int) bool { return s.Pages[i].Number < s.Pages[j].Number })
		if stored[key] == nil {
			stored[key] = map[string]Snapshot{}
		}
		if old, ok := stored[key][s.ID]; ok {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(s)
			if string(a) != string(b) {
				return p, errors.New("same snapshot ID has conflicting metadata")
			}
		}
		stored[key][s.ID] = s
	}
	routes, gbis, internal := map[string]bool{}, map[string]string{}, map[int64]string{}
	for _, r := range m.Routes {
		if !numeric.MatchString(r.STCIS) || r.Name == "" || routes[r.STCIS] || len(r.Regions) == 0 {
			return p, errors.New("invalid or duplicate route declaration")
		}
		routes[r.STCIS] = true
		verified := r.MappingStatus == "verified" && numeric.MatchString(r.GBIS) && r.Internal > 0 && sha.MatchString(r.EvidenceSHA)
		if verified {
			if old := gbis[r.GBIS]; old != "" && old != r.STCIS {
				return p, errors.New("GBIS route identity ambiguous")
			}
			gbis[r.GBIS] = r.STCIS
			if old := internal[r.Internal]; old != "" && old != r.STCIS {
				return p, errors.New("internal route identity ambiguous")
			}
			internal[r.Internal] = r.STCIS
		}
		regions := map[Region]bool{}
		for _, g := range r.Regions {
			if !numeric.MatchString(g.Province) || len(g.Province) != 2 || !numeric.MatchString(g.District) || len(g.District) != 5 || g.District[:2] != g.Province {
				return p, errors.New("invalid region code format")
			}
			regions[g] = true
		}
		if len(dates)*len(regions) > 100000-len(p.Tasks) {
			return p, errors.New("preview scope cap100000; split into explicit date batches")
		}
		for date := range dates {
			for g := range regions {
				s := routecongestion.Scope{Date: date, Route: r.STCIS, Province: g.Province, District: g.District}
				key := ScopeKey(s)
				t := Task{Scope: s, Key: key, State: "discovery_required", Mapping: r.MappingStatus, LegacyAllowed: s.Allowed(), Minimum: 1, Blockers: []string{}, dbQueries: 1}
				if !verified {
					t.Blockers = append(t.Blockers, "stcis_gbis_route_identity_not_verified")
				}
				if m.Origin == "synthetic_test" {
					t.Blockers = append(t.Blockers, "synthetic_fixture_not_official_evidence")
				}
				choices := stored[key]
				var chosen *Snapshot
				if id := m.Chosen[key]; id != "" {
					v, ok := choices[id]
					if !ok {
						return p, errors.New("explicit chosen snapshot missing")
					}
					chosen = &v
				} else if len(choices) == 1 {
					for _, v := range choices {
						copy := v
						chosen = &copy
					}
				} else if len(choices) > 1 {
					t.State = "snapshot_conflict"
					t.Minimum = 0
					t.Blockers = append(t.Blockers, "explicit_snapshot_selection_required")
				}
				if chosen != nil {
					t.Snapshot = chosen.ID
					t.State = chosen.State
					t.EstimateBasis = "validated_saved_snapshot_or_checkpoint"
					switch chosen.State {
					case "complete", "no_data_for_query":
						t.Minimum = 0
						t.Exact = ptr(0)
						t.Expected = ptr(0)
						t.remainingRawRows = ptr(0)
						if chosen.State == "complete" {
							t.ReusedRows = *chosen.Total
							t.ExpectedRows = ptr(*chosen.Total)
						} else {
							t.ExpectedRows = ptr(0)
						}
						if chosen.Ingested {
							t.ExpectedRows = ptr(0)
							t.dbQueries = 0
						}
					case "partial":
						n := pages(*chosen.Total) - int64(len(chosen.Pages))
						savedRows := int64(0)
						for _, page := range chosen.Pages {
							savedRows += page.Rows
						}
						if n == 0 {
							t.Blockers = append(t.Blockers, "local_checkpoint_seal_required")
						}
						t.Minimum = n
						t.Exact = ptr(n)
						t.Expected = ptr(n)
						t.ExpectedRows = ptr(*chosen.Total)
						t.remainingRawRows = ptr(*chosen.Total - savedRows)
					case "pending", "ambiguous", "failed":
						t.Minimum = 0
						t.Blockers = append(t.Blockers, "manual_failure_or_transmission_review_required")
					}
					if chosen.State == "partial" && chosen.Total != nil && pages(*chosen.Total) > m.Budget.MaxPages {
						t.Blockers = append(t.Blockers, "scope_page_stop_cap_insufficient")
						p.KnownPageCapViolations++
					}
				} else if t.State == "discovery_required" {
					if f, ok := forecasts[regionKey(r.STCIS, g)]; ok {
						t.Expected = ptr(pages(f.Rows))
						t.ExpectedRows = ptr(f.Rows)
						t.remainingRawRows = ptr(f.Rows)
						t.EstimateBasis = f.Basis
						if pages(f.Rows) > m.Budget.MaxPages {
							t.Blockers = append(t.Blockers, "forecast_exceeds_scope_stop_cap")
						}
					} else {
						t.EstimateBasis = "unknown; first-page discovery only"
					}
				}
				if len(t.Blockers) > 0 {
					p.BlockedScopes++
				}
				if t.Expected != nil && t.ExpectedRows != nil && t.remainingRawRows != nil {
					var e error
					t.Storage, e = estimate(*t.remainingRawRows, *t.Expected, *t.ExpectedRows, t.dbQueries, m.Model)
					if e != nil {
						return p, e
					}
				}
				p.Tasks = append(p.Tasks, t)
			}
		}
	}
	sort.Slice(p.Tasks, func(i, j int) bool { return p.Tasks[i].Key < p.Tasks[j].Key })
	raw, archive, low, high := 0.0, 0.0, 0.0, 0.0
	allStorage := true
	update := func(groups map[string]Rollup, key string, t Task) {
		g, ok := groups[key]
		if !ok {
			g.Expected = ptr(0)
			g.Rows = ptr(0)
		}
		g.Scopes++
		g.Minimum += t.Minimum
		g.Expected = sum(g.Expected, t.Expected)
		g.Rows = sum(g.Rows, t.ExpectedRows)
		groups[key] = g
	}
	for _, t := range p.Tasks {
		p.Minimum += t.Minimum
		p.Exact = sum(p.Exact, t.Exact)
		p.Expected = sum(p.Expected, t.Expected)
		p.ExpectedRows = sum(p.ExpectedRows, t.ExpectedRows)
		p.ReusedRows += t.ReusedRows
		if t.Storage == nil {
			allStorage = false
		} else {
			raw += float64(t.Storage.Raw)
			archive += float64(t.Storage.Archive)
			low += float64(t.Storage.DBLow)
			high += float64(t.Storage.DBHigh)
		}
		update(p.ByDate, t.Scope.Date, t)
		update(p.ByRoute, t.Scope.Route, t)
		update(p.ByRegion, t.Scope.Province+"/"+t.Scope.District, t)
	}
	if allStorage {
		for _, v := range []float64{raw, archive, low, high} {
			if v >= float64(math.MaxInt64) {
				return p, errors.New("aggregate storage overflow")
			}
		}
		p.Storage = &StorageEstimate{int64(raw), int64(archive), int64(low), int64(high)}
	}
	if p.Expected != nil {
		fits := *p.Expected <= m.Budget.MaxRequests
		p.FitsExpected = &fits
	}
	p.BudgetAssuresCompletion = p.Exact != nil && *p.Exact <= p.StopCap && p.BlockedScopes == 0 && p.KnownPageCapViolations == 0
	// This ID is a stable canonical preview, never an ingestion or approval token.
	canonical, _ := json.Marshal(struct {
		Tasks  []Task
		Budget Budget
		Model  StorageModel
		Origin string
	}{p.Tasks, m.Budget, m.Model, m.Origin})
	h := sha256.Sum256(canonical)
	p.ID = hex.EncodeToString(h[:])
	return p, nil
}

func (p Preview) String() string {
	return fmt.Sprintf("Offline scopes=%d minimum=%d expected=%v reused_rows=%d blocked=%d API calls=0", len(p.Tasks), p.Minimum, p.Expected, p.ReusedRows, p.BlockedScopes)
}
