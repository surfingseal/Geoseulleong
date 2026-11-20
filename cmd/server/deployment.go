package main

import (
	"errors"
	"strconv"
)

func serverAddress(get func(string) string) (string, error) {
	if port := get("PORT"); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("PORT must be 1..65535")
		}
		return "0.0.0.0:" + strconv.Itoa(n), nil
	}
	if addr := get("HTTP_ADDR"); addr != "" {
		return addr, nil
	}
	return "127.0.0.1:8090", nil
}

// Ephemeral dyno files must never be used as persistent API call ledgers.
// Live calls remain unavailable until the PostgreSQL budget integration is enabled.
func cloudPolicy(get func(string) string) error {
	if get("DYNO") == "" {
		return nil
	}
	if get("DATABASE_URL") == "" {
		return errors.New("Heroku requires DATABASE_URL")
	}
	if get("JOURNEY_SEARCH_MODE") == "live" || get("ALTERNATIVE_ACCESS_MODE") == "live" || get("BOARDING_REALTIME_ENABLED") == "true" || get("ALTERNATIVE_GBIS_ENABLED") == "true" {
		if get("API_BUDGET_BACKEND") != "postgres" {
			return errors.New("Heroku live APIs require PostgreSQL budgets")
		}
	}
	return nil
}
