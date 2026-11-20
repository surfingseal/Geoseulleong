package odsay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// SourceInfo is safe public provenance, never an authenticated request URL.
type SourceInfo struct {
	Mode              string    `json:"mode"`
	CapturedAt        time.Time `json:"captured_at"`
	ResponseSHA256    string    `json:"response_sha256"`
	CoordinateBinding string    `json:"coordinate_binding"`
}

type Recorded struct {
	response   Response
	start, end Point
	info       SourceInfo
}

func readBounded(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("recorded ODsay file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if e != nil || len(b) > 4<<20 {
		return nil, ErrSchema
	}
	return b, nil
}

// LoadRecorded validates the stored live response, without any HTTP client or API key.
func LoadRecorded(dir string) (*Recorded, error) {
	meta, e := readBounded(filepath.Join(dir, "metadata.json"))
	if e != nil {
		return nil, e
	}
	var m struct {
		HTTP     int       `json:"http_status"`
		Status   string    `json:"status"`
		SHA      string    `json:"response_sha256"`
		Received time.Time `json:"received_at"`
		Request  struct {
			Endpoint                   string `json:"endpoint"`
			SX, SY, EX, EY             float64
			SearchType, SearchPathType int
		} `json:"request"`
	}
	if json.Unmarshal(meta, &m) != nil || m.HTTP != 200 || m.Status != "complete" || m.Received.IsZero() || m.Request.Endpoint != Endpoint || m.Request.SearchType != 0 || m.Request.SearchPathType != 0 {
		return nil, ErrSchema
	}
	raw, e := readBounded(filepath.Join(dir, "raw-response.json"))
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != m.SHA {
		return nil, errors.New("recorded ODsay checksum mismatch")
	}
	response, e := Parse(raw)
	if e != nil {
		return nil, e
	}
	start, end := Point{m.Request.SX, m.Request.SY}, Point{m.Request.EX, m.Request.EY}
	if !(Query{Start: start, End: end, Mode: "all"}).Valid() {
		return nil, ErrInput
	}
	return &Recorded{response: response, start: start, end: end, info: SourceInfo{Mode: "recorded_live_response_offline", CapturedAt: m.Received, ResponseSHA256: m.SHA, CoordinateBinding: "exact_recorded_origin_and_destination"}}, nil
}
func (r *Recorded) Search(ctx context.Context, q Query) (Response, string, error) {
	if q.Mode == "walk" {
		return Response{}, "odsay_recorded_response", ErrUnsupportedMode
	}
	if !q.Valid() || q.Start != r.start || q.End != r.end {
		return Response{}, "odsay_recorded_response", ErrInput
	}
	if e := ctx.Err(); e != nil {
		return Response{}, "odsay_recorded_response", e
	}
	return r.response, "odsay_recorded_response", nil
}
func (r *Recorded) Provenance() SourceInfo { return r.info }

// CoordinatesQuery returns only recorded, already validated coordinates for offline consumers.
func (r *Recorded) CoordinatesQuery() Query { return Query{Start: r.start, End: r.end, Mode: "all"} }
