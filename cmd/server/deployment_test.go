package main

import "testing"

func TestServerAddress(t *testing.T) {
	for _, tc := range []struct {
		port, addr, want string
		invalid          bool
	}{{"5000", "127.0.0.1:8090", "0.0.0.0:5000", false}, {"", "", "127.0.0.1:8090", false}, {"bad", "", "", true}, {"0", "", "", true}} {
		get := func(k string) string {
			if k == "PORT" {
				return tc.port
			}
			if k == "HTTP_ADDR" {
				return tc.addr
			}
			return ""
		}
		got, e := serverAddress(get)
		if (e != nil) != tc.invalid || got != tc.want {
			t.Fatalf("address %q", got)
		}
	}
}
func TestCloudRejectsEphemeralLiveBudget(t *testing.T) {
	env := map[string]string{"DYNO": "web.1", "DATABASE_URL": "configured", "JOURNEY_SEARCH_MODE": "disabled"}
	get := func(k string) string { return env[k] }
	if cloudPolicy(get) != nil {
		t.Fatal("disabled blocked")
	}
	env["JOURNEY_SEARCH_MODE"] = "live"
	if cloudPolicy(get) == nil {
		t.Fatal("file budget allowed")
	}
	env["API_BUDGET_BACKEND"] = "postgres"
	env["BUDGET_DATABASE_URL"] = "configured"
	if cloudPolicy(get) != nil {
		t.Fatal("durable policy rejected")
	}
}
