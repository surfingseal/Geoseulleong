// Package journeysource selects a provider without enabling live requests by default.
package journeysource

import (
	"context"
	"geoseulleong/internal/kakao"
	"geoseulleong/internal/odsay"
	"net"
	"path/filepath"
	"strconv"
	"time"
)

func New(get func(string) string, addr string, budget ...func(string) func(context.Context) error) (odsay.Searcher, func(), error) {
	noop := func() {}
	var reserveCall func(context.Context) error
	reserveFor := func(provider string) []func(context.Context) error {
		if len(budget) > 0 {
			return []func(context.Context) error{budget[0](provider)}
		}
		return nil
	}
	provider, mode := get("JOURNEY_PROVIDER"), get("JOURNEY_SEARCH_MODE")
	if provider == "" {
		provider = "kakao"
	}
	if provider != "kakao" && provider != "odsay" && provider != "recorded" {
		return nil, noop, odsay.ErrInput
	}
	if mode == "" {
		mode = "disabled"
		if provider == "recorded" || get("JOURNEY_REPLAY_DIR") != "" {
			mode = "recorded"
		}
	}
	if mode == "disabled" {
		if get("JOURNEY_REPLAY_DIR") != "" {
			return nil, noop, odsay.ErrInput
		}
		return odsay.Disabled{}, noop, nil
	}
	if mode == "recorded" {
		env := func(k string) string {
			if k == "JOURNEY_SEARCH_MODE" {
				return "recorded"
			}
			return get(k)
		}
		return odsay.NewServerSearcher(env, addr)
	}
	if mode != "live" || provider == "recorded" {
		return nil, noop, odsay.ErrInput
	}
	if provider == "odsay" {
		return odsay.NewServerSearcher(get, addr, reserveFor("odsay")...)
	}
	if get("JOURNEY_REPLAY_DIR") != "" {
		return nil, noop, odsay.ErrInput
	}
	host, _, e := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	if e != nil || !(host == "localhost" || ip != nil && ip.IsLoopback() || len(budget) > 0 && budget[0]("kakao") != nil) {
		return nil, noop, odsay.ErrInput
	}
	if get("KAKAO_LIVE_APPROVED") != "true" {
		return nil, noop, odsay.ErrDisabled
	}
	daily, e1 := strconv.Atoi(get("KAKAO_DAILY_LIMIT"))
	total, e2 := strconv.Atoi(get("KAKAO_TOTAL_LIMIT"))
	reserve, e3 := strconv.Atoi(get("KAKAO_EXTERNAL_DAILY_RESERVE"))
	ttl := 300
	if v := get("KAKAO_CACHE_TTL_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, noop, odsay.ErrInput
		}
		ttl = n
	}
	state := get("KAKAO_BUDGET_DIR")
	if e1 != nil || e2 != nil || e3 != nil || (!filepath.IsAbs(state) && len(budget) == 0) {
		return nil, noop, odsay.ErrInput
	}
	if len(budget) > 0 {
		reserveCall = budget[0]("kakao")
	}
	p, e := kakao.NewLive(get("KAKAO_REST_API_KEY"), odsay.LiveConfig{Approved: true, DailyLimit: daily, TotalLimit: total, ExternalReserve: reserve, CacheTTL: time.Duration(ttl) * time.Second, StateDir: state, Reserve: reserveCall})
	if e != nil {
		return nil, noop, e
	}
	return p, func() { _ = p.Close() }, nil
}
