package main

import (
	"context"
	"encoding/base64"
	"errors"
	"geoseulleong/internal/alternatives"
	"geoseulleong/internal/auth"
	"geoseulleong/internal/boardingruntime"
	"geoseulleong/internal/database"
	"geoseulleong/internal/httpapi"
	"geoseulleong/internal/journeysource"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"geoseulleong/internal/repository"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		// run returns sanitized configuration/connection errors, never credentials.
		log.Printf("server stopped: %v", e)
		os.Exit(1)
	}
}
func run() error {
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, e := database.Open(root)
	if e != nil {
		return e
	}
	defer pool.Close()
	addr, e := serverAddress(os.Getenv)
	if e != nil {
		return e
	}
	if e := cloudPolicy(os.Getenv); e != nil {
		return e
	}
	budgetFor, selectionStore, closeBudget, err := deploymentBudgets(root)
	if err != nil {
		return err
	}
	defer closeBudget()
	if os.Getenv("DYNO") != "" && selectionStore == nil {
		return errors.New("Heroku requires PostgreSQL journey selections")
	}
	var budgetOptions []budgetFactory
	if budgetFor != nil {
		budgetOptions = append(budgetOptions, budgetFor)
	}
	searcher, closeSearcher, err := journeysource.New(os.Getenv, addr, budgetOptions...)
	if err != nil {
		return errors.New("journey search mode configuration invalid")
	}
	defer closeSearcher()
	cfg, e := boardingruntime.ReadConfig(os.Getenv)
	if e != nil {
		return e
	}
	if budgetFor != nil && cfg.RealtimeEnabled {
		cfg.ReserveCall = budgetFor("gbis")
		cfg.PersistentDaily, _ = strconv.Atoi(os.Getenv("GBIS_VALIDATION_DAILY_LIMIT"))
		cfg.PersistentTotal, _ = strconv.Atoi(os.Getenv("GBIS_VALIDATION_TOTAL_LIMIT"))
	}
	boardingService, e := boardingruntime.Build(root, pool, cfg)
	if e != nil {
		return e
	}
	weightsPath := os.Getenv("JOURNEY_SCORING_CONFIG")
	if weightsPath == "" {
		weightsPath = "config/boarding-aware.json"
	}
	weights, e := odsay.LoadBoardingWeights(weightsPath)
	if e != nil {
		return e
	}
	selections := odsay.NewSelections()
	if selectionStore != nil {
		selections = odsay.NewPersistentSelections(selectionStore)
	}
	journeys := &odsay.Service{Selections: selections, BoardingWeights: &weights, Searcher: searcher, Reader: repository.NewJourneyReferences(pool), Boarding: boardingService}

	alternativeService := &alternatives.Service{Selections: selections, Source: repository.NewAlternativesSource(pool, os.Getenv("ALTERNATIVE_BOARDING_FILES")), Identity: repository.NewJourneyReferences(pool)}
	// Recorded mode retains legacy index-only calls; all live providers use the
	// same server-owned search selection registry populated by /journeys.
	if recorded, ok := searcher.(*odsay.Recorded); ok {
		paths, _, err := recorded.Search(root, recorded.CoordinatesQuery())
		if err != nil {
			return err
		}
		alternativeService.Recorded = paths
		origin := recorded.CoordinatesQuery().Start
		alternativeService.Origin = &origin
		destination := recorded.CoordinatesQuery().End
		alternativeService.Destination = &destination
		captured := recorded.Provenance().CapturedAt
		alternativeService.RecordedAt = &captured
		alternativeService.DataOrigin = "odsay_recorded_response"
	}
	closeAlternatives, err := configureAlternativeLive(root, alternativeService, addr, searcher, budgetOptions...)
	if err != nil {
		return err
	}
	defer closeAlternatives()
	if manifest := os.Getenv("ALTERNATIVE_REACHABILITY_MANIFEST"); manifest != "" {
		replay, err := reachability.LoadReplay(manifest)
		if err != nil {
			return errors.New("GBIS replay configuration invalid")
		}
		alternativeService.ReachProvider = reachability.NewCached(replay)
	}
	if manifest := os.Getenv("ALTERNATIVE_SEGMENT_MANIFEST"); manifest != "" {
		replay, err := alternatives.LoadAccessReplay(manifest)
		if err != nil {
			return errors.New("segment replay configuration invalid")
		}
		alternativeService.SavedSegments = replay
	}
	if manifest := os.Getenv("ALTERNATIVE_ACCESS_MANIFEST"); manifest != "" {
		replay, err := alternatives.LoadAccessReplay(manifest)
		if err != nil {
			return errors.New("access replay configuration invalid")
		}
		alternativeService.AccessPlanner = &alternatives.AccessPlanner{Searcher: replay}
	}
	var authService *auth.Service
	switch os.Getenv("AUTH_ENABLED") {
	case "", "false":
	case "true":
		secret, err := base64.StdEncoding.DecodeString(os.Getenv("AUTH_JWT_SECRET_BASE64"))
		if err != nil || len(secret) < 32 {
			return errors.New("auth signing secret configuration invalid")
		}
		authPool, err := database.OpenAuth(root)
		if err != nil {
			return err
		}
		defer authPool.Close()
		authService, err = auth.New(repository.NewAuth(authPool), auth.Config{Secret: secret, TermsVersion: os.Getenv("AUTH_TERMS_VERSION"), PrivacyVersion: os.Getenv("AUTH_PRIVACY_VERSION")})
		if err != nil {
			return err
		}
	default:
		return errors.New("AUTH_ENABLED must be true or false")
	}
	handler := httpapi.WithAuth(httpapi.NewWithAlternatives(repository.NewRoutes(pool), repository.NewCongestion(pool), journeys, alternativeService), authService)
	ln, e := net.Listen("tcp", addr)
	if e != nil {
		return errors.New("listen failed")
	}
	handler = httpapi.WithHealth(handler, pool)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return root }}
	result := make(chan error, 1)
	go func() { result <- server.Serve(ln) }()
	log.Print("read-only route API listening")
	select {
	case e = <-result:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server failed")
	case <-root.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e = server.Shutdown(ctx); e != nil {
			_ = server.Close()
		}
		<-result
		log.Print("HTTP server and database pool stopping")
		return nil
	}
}
