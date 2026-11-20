package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"geoseulleong/internal/alternatives"
	"geoseulleong/internal/database"
	"geoseulleong/internal/httpapi"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"geoseulleong/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explicit isolated session writes; existing transport/statistics are SELECT only.
func TestRecordedSearchAcrossSelectionPoolRestart(t *testing.T) {
	raw := os.Getenv("TEST_SELECTION_DATABASE_URL")
	if raw == "" {
		t.Skip("explicit isolated selection DB required")
	}
	cfg, e := pgxpool.ParseConfig(raw)
	if e != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "geoseulleong_selection_test_") || !(cfg.ConnConfig.Host == "/tmp" || cfg.ConnConfig.Host == "127.0.0.1") {
		t.Fatal("isolated loopback DB required")
	}
	ctx := context.Background()
	open := func() *pgxpool.Pool {
		p, e := pgxpool.NewWithConfig(ctx, cfg.Copy())
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	pool := open()
	storage, e := repository.NewJourneySelections(ctx, pool)
	if e != nil {
		t.Fatal(e)
	}
	reader, e := database.Open(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	recorded, e := odsay.LoadRecorded("../../results/odsay-live-20261113-single-02")
	if e != nil {
		t.Fatal(e)
	}
	selection := odsay.NewPersistentSelections(storage)
	journeys := &odsay.Service{Selections: selection, Searcher: recorded, Reader: repository.NewJourneyReferences(reader)}
	handler := httpapi.NewWithJourneys(nil, nil, journeys)
	q := recorded.CoordinatesQuery()
	q.Date = "2026-10-01"
	q.Set = "da665031297012483299c96fc3292fa2638462f4b1ffb7059692e3e454159a91"
	args := url.Values{"start_lon": {strconv.FormatFloat(q.Start.Lon, 'f', -1, 64)}, "start_lat": {strconv.FormatFloat(q.Start.Lat, 'f', -1, 64)}, "end_lon": {strconv.FormatFloat(q.End.Lon, 'f', -1, 64)}, "end_lat": {strconv.FormatFloat(q.End.Lat, 'f', -1, 64)}, "reference_date": {q.Date}, "analysis_set_id": {q.Set}}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/journeys?"+args.Encode(), nil))
	if w.Code != 200 {
		t.Fatalf("recorded search %d %s", w.Code, w.Body.String())
	}
	var found odsay.Candidates
	if json.Unmarshal(w.Body.Bytes(), &found) != nil || len(found.Paths) != 8 || found.SearchID == "" {
		t.Fatal("search contract")
	}
	pool.Close() // Simulate dyno restart: all selection memory and connections are discarded.
	pool = open()
	defer pool.Close()
	storage, e = repository.NewJourneySelections(ctx, pool)
	if e != nil {
		t.Fatal(e)
	}
	selection = odsay.NewPersistentSelections(storage)
	if _, ok, e := selection.GetContext(ctx, found.SearchID); e != nil || !ok {
		t.Fatal("selection lost across restart")
	}
	access, e := alternatives.LoadAccessReplay("../reachability/testdata/access-manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	realtime, e := reachability.LoadReplay("../reachability/testdata/recorded-gbis-manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	segments, e := alternatives.LoadAccessReplay("../../results/odsay-express-remaining-20261120-160424/segments-manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	svc := &alternatives.Service{SavedSegments: segments, Selections: selection, Source: repository.NewAlternativesSource(reader, ""), Identity: repository.NewJourneyReferences(reader), AccessPlanner: &alternatives.AccessPlanner{Searcher: access}, ReachProvider: reachability.NewCached(realtime)}
	handler = httpapi.NewWithAlternatives(nil, nil, &odsay.Service{Searcher: odsay.Disabled{}}, svc)
	checked := 0
	for _, path := range found.Paths {
		for _, leg := range path.Legs {
			for _, option := range leg.Options {
				if option.Lane.BusNumber != "M5121" {
					continue
				}
				a := url.Values{"search_id": {found.SearchID}, "path_index": {strconv.Itoa(path.PathIndex)}, "leg_index": {strconv.Itoa(leg.Index)}, "service_date": {q.Date}, "tzon": {"07"}}
				alt := httptest.NewRecorder()
				handler.ServeHTTP(alt, httptest.NewRequest("GET", "/api/v1/journeys/boarding-recommendations?"+a.Encode(), nil))
				if alt.Code != 200 {
					t.Fatalf("alternative after restart %d %s", alt.Code, alt.Body.String())
				}
				var result alternatives.Result
				if json.Unmarshal(alt.Body.Bytes(), &result) != nil || result.Original.ID != 6 || len(result.Candidates) != 3 {
					t.Fatal("wrong selected visit")
				}
				for _, candidate := range result.Candidates {
					if candidate.ExpressSegment == nil || candidate.ExpressSegment.Minutes == nil || candidate.Itinerary == nil || candidate.Itinerary.TotalMinutes == nil {
						t.Fatal("saved express duration not reused")
					}
				}
				checked++
				t.Logf("path=%d leg=%d visit=%d candidates=%d", path.PathIndex, leg.Index, result.Original.ID, len(result.Candidates))
				if dir := os.Getenv("TEST_JOURNEY_OUTPUT_DIR"); dir != "" {
					if e := os.MkdirAll(dir, 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(dir, "journeys-recorded.json"), w.Body.Bytes(), 0600); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(dir, "recommendations-recorded.json"), alt.Body.Bytes(), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("M5121 not exercised")
	}
	_, e = pool.Exec(ctx, `UPDATE journey_selections SET created_at=now()-interval '11 minutes',expires_at=now()-interval '1 minute' WHERE search_id=$1`, found.SearchID)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok, e := selection.GetContext(ctx, found.SearchID); e != nil || ok {
		t.Fatal("expired selection accepted")
	}
	pool.Close()
	if _, _, e := selection.GetContext(ctx, found.SearchID); !errors.Is(e, odsay.ErrSelectionStore) {
		t.Fatal("storage outage not distinguished", e)
	}
	t.Logf("recorded_paths=8 restored_M5121_legs=%d external_calls=0 TTL=%s", checked, 10*time.Minute)
}
