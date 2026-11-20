package database

import (
	"strings"
	"testing"
)

func TestDeploymentTLS(t *testing.T) {
	t.Setenv("DYNO", "web.1")
	raw, e := deploymentDSN("postgres://user:secret@example.invalid/db")
	if e != nil || !strings.Contains(raw, "sslmode=require") {
		t.Fatal("TLS required")
	}
	for _, mode := range []string{"disable", "allow", "prefer"} {
		if _, e = deploymentDSN("postgres://example.invalid/db?sslmode=" + mode); e == nil {
			t.Fatal("plaintext fallback allowed")
		}
	}
	for _, mode := range []string{"require", "verify-full"} {
		if _, e = deploymentDSN("postgres://example.invalid/db?sslmode=" + mode); e != nil {
			t.Fatal("TLS rejected")
		}
	}
}
