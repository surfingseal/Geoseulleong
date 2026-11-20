package odsay

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"time"
)

// NewServerSearcher never reads .env. The operator must explicitly supply server environment.
func NewServerSearcher(getenv func(string) string, address string, budget ...func(context.Context) error) (Searcher, func(), error) {
	noop := func() {}
	mode, dir := getenv("JOURNEY_SEARCH_MODE"), getenv("JOURNEY_REPLAY_DIR")
	// Preserve the previously explicit replay-directory switch.
	if mode == "" {
		mode = "disabled"
		if dir != "" {
			mode = "recorded"
		}
	}
	if mode == "disabled" {
		if dir != "" {
			return nil, noop, ErrInput
		}
		return Disabled{}, noop, nil
	}
	host, _, e := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if e != nil || !(host == "localhost" || ip != nil && ip.IsLoopback() || mode == "live" && len(budget) > 0 && budget[0] != nil) {
		return nil, noop, errors.New("journey mode must bind loopback")
	}
	switch mode {
	case "recorded":
		if dir == "" {
			return nil, noop, ErrInput
		}
		source, e := LoadRecorded(filepath.Clean(dir))
		return source, noop, e
	case "live":
		if dir != "" {
			return nil, noop, ErrInput
		}
		if getenv("ODSAY_LIVE_APPROVED") != "true" {
			return nil, noop, ErrDisabled
		}
		daily, e1 := strconv.Atoi(getenv("ODSAY_DAILY_LIMIT"))
		total, e2 := strconv.Atoi(getenv("ODSAY_TOTAL_LIMIT"))
		reserve, e3 := strconv.Atoi(getenv("ODSAY_EXTERNAL_DAILY_RESERVE"))
		ttl := 300
		if value := getenv("ODSAY_CACHE_TTL_SECONDS"); value != "" {
			var e error
			ttl, e = strconv.Atoi(value)
			if e != nil {
				return nil, noop, ErrInput
			}
		}
		state := getenv("ODSAY_BUDGET_DIR")
		if e1 != nil || e2 != nil || e3 != nil || (!filepath.IsAbs(state) && len(budget) == 0) {
			return nil, noop, ErrInput
		}
		var reserveCall func(context.Context) error
		if len(budget) > 0 {
			reserveCall = budget[0]
		}
		source, e := NewLive(getenv("ODSAY_API_KEY"), LiveConfig{Approved: true, DailyLimit: daily, TotalLimit: total, ExternalReserve: reserve, CacheTTL: time.Duration(ttl) * time.Second, StateDir: state, Reserve: reserveCall})
		if e != nil {
			return nil, noop, e
		}
		return source, func() { _ = source.Close() }, nil
	default:
		return nil, noop, ErrInput
	}
}
