package main

import (
	"context"
	"errors"
	"geoseulleong/internal/database"
	"geoseulleong/internal/gbis"
	"geoseulleong/internal/odsay"
	"geoseulleong/internal/repository"
	"geoseulleong/internal/requestbudget"
	"os"
	"strconv"
	"time"
)

type budgetFactory = func(string) func(context.Context) error

// The existing writable budget pool also stores short-lived selections. No migrations
// or zero-counter seeds are performed at startup.
func deploymentBudgets(ctx context.Context) (budgetFactory, odsay.SelectionStore, func(), error) {
	noop := func() {}
	if os.Getenv("API_BUDGET_BACKEND") != "postgres" {
		return nil, nil, noop, nil
	}
	p, e := database.OpenBudget(ctx)
	if e != nil {
		return nil, nil, noop, e
	}
	selections, e := repository.NewJourneySelections(ctx, p)
	if e != nil {
		p.Close()
		return nil, nil, noop, errors.New("journey selection database migration required")
	}
	store := requestbudget.Store{Pool: p}
	factory := func(provider string) func(context.Context) error {
		prefix := map[string]string{"odsay": "ODSAY", "kakao": "KAKAO", "gbis": "GBIS_VALIDATION"}[provider]
		keyName := map[string]string{"odsay": "ODSAY_API_KEY", "kakao": "KAKAO_REST_API_KEY", "gbis": "GBIS_API_KEY"}[provider]
		daily, e1 := strconv.Atoi(os.Getenv(prefix + "_DAILY_LIMIT"))
		total, e2 := strconv.Atoi(os.Getenv(prefix + "_TOTAL_LIMIT"))
		reserve := 0
		var e3 error
		if v := os.Getenv(prefix + "_EXTERNAL_DAILY_RESERVE"); v != "" {
			reserve, e3 = strconv.Atoi(v)
		}
		key := os.Getenv(keyName)
		return func(c context.Context) error {
			if prefix == "" || key == "" || e1 != nil || e2 != nil || e3 != nil {
				return errors.New("persistent API budget configuration invalid")
			}
			bounded, cancel := context.WithTimeout(c, 10*time.Second)
			defer cancel()
			err := store.Reserve(bounded, requestbudget.Scope(provider, key), requestbudget.Limits{Daily: daily, Total: total, ExternalReserve: reserve})
			if err == nil {
				return nil
			}
			if provider == "gbis" {
				kind := "budget_state_unavailable"
				if errors.Is(err, requestbudget.ErrExhausted) {
					kind = "persistent_budget_exhausted"
				}
				return &gbis.Error{Kind: kind}
			}
			if errors.Is(err, requestbudget.ErrExhausted) {
				return odsay.ErrBudget
			}
			return odsay.ErrState
		}
	}
	return factory, selections, p.Close, nil
}
