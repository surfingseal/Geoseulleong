package main

import (
	"context"
	"errors"
	"geoseulleong/internal/alternatives"
	"geoseulleong/internal/gbis"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/reachability"
	"os"
	"strconv"
)

func configureAlternativeLive(_ context.Context, s *alternatives.Service, addr string, journeySearcher odsay.Searcher, budgets ...budgetFactory) (func(), error) {
	var odsayBudget []func(context.Context) error
	var gbisBudget func(context.Context) error
	if len(budgets) > 0 {
		odsayBudget = []func(context.Context) error{budgets[0]("odsay")}
		gbisBudget = budgets[0]("gbis")
	}
	closeAll := []func(){}
	close := func() {
		for _, f := range closeAll {
			f()
		}
	}
	sharedLive, canShare := journeySearcher.(*odsay.Live)
	canShare = canShare && os.Getenv("JOURNEY_PROVIDER") == "odsay"
	accessMode := os.Getenv("ALTERNATIVE_ACCESS_MODE")
	if accessMode == "live" || accessMode == "" && canShare && os.Getenv("ALTERNATIVE_ACCESS_MANIFEST") == "" {
		if os.Getenv("ALTERNATIVE_ACCESS_MANIFEST") != "" {
			return close, errors.New("choose live or recorded access, not both")
		}
		if canShare {
			s.AccessPlanner = &alternatives.AccessPlanner{Searcher: sharedLive}
		} else {
			get := func(k string) string {
				if k == "JOURNEY_SEARCH_MODE" {
					return "live"
				}
				if k == "JOURNEY_REPLAY_DIR" {
					return ""
				}
				return os.Getenv(k)
			}
			searcher, done, e := odsay.NewServerSearcher(get, addr, odsayBudget...)
			if e != nil {
				return close, e
			}
			closeAll = append(closeAll, done)
			s.AccessPlanner = &alternatives.AccessPlanner{Searcher: searcher}
		}
	} else if v := os.Getenv("ALTERNATIVE_ACCESS_MODE"); v != "" && v != "recorded" {
		return close, errors.New("invalid alternative access mode")
	}
	if os.Getenv("ALTERNATIVE_GBIS_ENABLED") == "true" {
		if os.Getenv("ALTERNATIVE_REACHABILITY_MANIFEST") != "" {
			close()
			return func() {}, errors.New("choose live or recorded GBIS, not both")
		}
		daily, e1 := strconv.Atoi(os.Getenv("GBIS_VALIDATION_DAILY_LIMIT"))
		total, e2 := strconv.Atoi(os.Getenv("GBIS_VALIDATION_TOTAL_LIMIT"))
		if e1 != nil || e2 != nil {
			close()
			return func() {}, errors.New("explicit GBIS daily/total limits required")
		}
		client, budget, e := gbis.NewPersistentRealtimeClient(os.Getenv("GBIS_API_KEY"), os.Getenv("GBIS_VALIDATION_APPROVED") == "true", gbis.ValidationBudgetConfig{Directory: os.Getenv("GBIS_VALIDATION_BUDGET_DIR"), DailyLimit: daily, TotalLimit: total, ReserveCall: gbisBudget})
		if e != nil {
			close()
			return func() {}, e
		}
		closeAll = append(closeAll, func() { budget.Close() })
		s.ReachProvider = reachability.NewCached(reachability.GBISAdapter{Client: client})
	} else if v := os.Getenv("ALTERNATIVE_GBIS_ENABLED"); v != "" && v != "false" {
		close()
		return func() {}, errors.New("invalid alternative GBIS flag")
	}
	return close, nil
}
