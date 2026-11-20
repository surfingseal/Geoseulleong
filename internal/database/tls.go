package database

import (
	"net/url"
	"os"
)

// Heroku Postgres requires TLS. sslmode=require encrypts the connection;
// operators with a trusted CA may explicitly use verify-ca or verify-full.
func deploymentDSN(raw string) (string, error) {
	if os.Getenv("DYNO") == "" {
		return raw, nil
	}
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return "", ErrConfig
	}
	q := u.Query()
	switch q.Get("sslmode") {
	case "":
		q.Set("sslmode", "require")
	case "require", "verify-ca", "verify-full":
	default:
		return "", ErrConfig
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
