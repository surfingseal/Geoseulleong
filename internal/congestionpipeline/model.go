// Package congestionpipeline separates immutable observations from assumed interpretation.
package congestionpipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/routecongestion"
	"math"
	"strconv"
	"strings"
	"time"
)

const PolicyVersion = "cgst-percent-v1"

var Policy = json.RawMessage(`{"version":"cgst-percent-v1","status":"hypothesis_unverified","assumed_unit":"percent","formula":"onboard_passengers / vehicle_capacity * 100","officially_confirmed":false,"boarding_failure_probability":false}`)

type Page struct {
	Number int               `json:"page"`
	HTTP   int               `json:"http_status"`
	Raw    []byte            `json:"raw_response_base64"`
	SHA    string            `json:"sha256"`
	At     time.Time         `json:"collected_at"`
	Rows   []json.RawMessage `json:"records"`
}
type Batch struct {
	ID       string                `json:"batch_id"`
	Scope    routecongestion.Scope `json:"scope"`
	Total    int                   `json:"total_count"`
	Pages    []Page                `json:"pages"`
	Policy   json.RawMessage       `json:"interpretation_policy"`
	Complete bool                  `json:"complete"`
}

func scalar(raw json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
			return string(v)
		}
	}
	return ""
}
func Value(raw json.RawMessage, key string) string { return scalar(raw, key) }
func Check(raw []byte, status int, s routecongestion.Scope, number int) (routecongestion.Page, error) {
	return CheckAuthorized(raw, status, s, number, func(s routecongestion.Scope) bool { return s.Allowed() })
}

// CheckAuthorized shares strict response validation with a separately verified scope gate.
func CheckAuthorized(raw []byte, status int, s routecongestion.Scope, number int, allowed func(routecongestion.Scope) bool) (routecongestion.Page, error) {
	p, e := routecongestion.Parse(raw)
	if status != 200 {
		return p, errors.New("HTTP status failure")
	}
	if e != nil {
		return p, e
	}
	if allowed == nil || !allowed(s) {
		return p, errors.New("scope not approved")
	}
	if p.Number != number || p.Size != 1000 {
		return p, errors.New("page metadata mismatch")
	}
	if number > (p.Total+999)/1000 && p.Total != 0 {
		return p, errors.New("page outside total")
	}
	expected := 1000
	if remain := p.Total - (number-1)*1000; remain < expected {
		expected = remain
	}
	if expected < 0 || len(p.Rows) != expected {
		return p, errors.New("page count mismatch")
	}
	for _, r := range p.Rows {
		if scalar(r, "opr_ymd") != s.Date || scalar(r, "rte_id") != s.Route || scalar(r, "ctpv_cd") != s.Province || scalar(r, "sgg_cd") != s.District {
			return p, errors.New("row date/route/region mismatch")
		}
	}
	return p, nil
}
func Build(s routecongestion.Scope, pages []Page) (Batch, error) {
	return BuildAuthorized(s, pages, func(s routecongestion.Scope) bool { return s.Allowed() })
}

// BuildAuthorized never changes the legacy Build allowlist.
func BuildAuthorized(s routecongestion.Scope, pages []Page, allowed func(routecongestion.Scope) bool) (Batch, error) {
	b := Batch{Scope: s, Pages: pages, Policy: append(json.RawMessage(nil), Policy...)}
	if len(pages) == 0 {
		return b, errors.New("no pages")
	}
	total := -1
	count := 0
	hash := sha256.New()
	scope, _ := json.Marshal(s)
	hash.Write(scope)
	for i := range b.Pages {
		p := &b.Pages[i]
		v, e := CheckAuthorized(p.Raw, p.HTTP, s, i+1, allowed)
		if e != nil {
			return b, e
		}
		if total < 0 {
			total = v.Total
		}
		if v.Total != total {
			return b, errors.New("total changed")
		}
		p.Number = i + 1
		p.Rows = v.Rows
		sum := sha256.Sum256(p.Raw)
		actual := hex.EncodeToString(sum[:])
		if p.SHA != "" && p.SHA != actual {
			return b, errors.New("page checkpoint hash mismatch")
		}
		p.SHA = actual
		hash.Write([]byte(actual))
		count += len(p.Rows)
	}
	if count != total || len(pages) != max(1, (total+999)/1000) {
		return b, errors.New("incomplete batch")
	}
	b.Total = total
	b.Complete = true
	b.ID = hex.EncodeToString(hash.Sum(nil))
	return b, nil
}

type Aggregate struct {
	Count       int      `json:"count"`
	Numeric     int      `json:"numeric_cgst_count"`
	Missing     int      `json:"missing_cgst_count"`
	Min         *float64 `json:"cgst_min"`
	Max         *float64 `json:"cgst_max"`
	AssumedMean *float64 `json:"mean_percent_hypothesis"`
	Invalid     int      `json:"invalid_cgst_count"`
	sum         float64
}

func (a *Aggregate) add(raw json.RawMessage) {
	a.Count++
	value := scalar(raw, "cgst")
	if value == "" || value == "null" {
		a.Missing++
		return
	}
	v, e := strconv.ParseFloat(value, 64)
	if e != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		a.Invalid++
		return
	}
	a.Numeric++
	a.sum += v
	if a.Min == nil || v < *a.Min {
		x := v
		a.Min = &x
	}
	if a.Max == nil || v > *a.Max {
		x := v
		a.Max = &x
	}
	x := a.sum / float64(a.Numeric)
	a.AssumedMean = &x
}

type Analysis struct {
	Records               int                   `json:"records"`
	Regions               map[string]*Aggregate `json:"by_region"`
	Stops                 map[string]*Aggregate `json:"by_stop"`
	Times                 map[string]*Aggregate `json:"by_time_code"`
	Trips                 map[string]int        `json:"trip_ordinal_counts"`
	RepeatedExcess        int                   `json:"identical_json_excess_observations"`
	MultiBuckets          int                   `json:"multiple_observation_stop_time_buckets"`
	Policy                json.RawMessage       `json:"policy"`
	RouteCoverageComplete bool                  `json:"whole_route_coverage_complete"`
}

func Analyze(batches []Batch) Analysis {
	a := Analysis{Regions: map[string]*Aggregate{}, Stops: map[string]*Aggregate{}, Times: map[string]*Aggregate{}, Trips: map[string]int{}, Policy: Policy}
	seen := map[string]int{}
	buckets := map[string]int{}
	for _, b := range batches {
		for _, p := range b.Pages {
			for _, r := range p.Rows {
				a.Records++
				for _, group := range []struct {
					m map[string]*Aggregate
					k string
				}{{a.Regions, b.Scope.Province + "/" + b.Scope.District}, {a.Stops, scalar(r, "sttn_id")}, {a.Times, scalar(r, "tzon")}} {
					if group.m[group.k] == nil {
						group.m[group.k] = &Aggregate{}
					}
					group.m[group.k].add(r)
				}
				a.Trips[scalar(r, "opr_trntm")]++
				var v any
				json.Unmarshal(r, &v)
				canonical, _ := json.Marshal(v)
				seen[string(canonical)]++
				buckets[fmt.Sprint(b.Scope.Province, "/", b.Scope.District, "/", scalar(r, "sttn_id"), "/", scalar(r, "tzon"))]++
			}
		}
	}
	for _, n := range seen {
		a.RepeatedExcess += n - 1
	}
	for _, n := range buckets {
		if n > 1 {
			a.MultiBuckets++
		}
	}
	return a
}
