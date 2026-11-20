package routecongestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type PageLog struct {
	Page  int    `json:"page"`
	HTTP  int    `json:"http_status"`
	Code  string `json:"api_code"`
	Rows  int    `json:"rows"`
	Error string `json:"error,omitempty"`
}
type Collection struct {
	Scope    Scope             `json:"scope"`
	Total    *int              `json:"reported_total_count"`
	Pages    *int              `json:"required_pages"`
	Records  []json.RawMessage `json:"records"`
	Logs     []PageLog         `json:"page_logs"`
	Calls    int               `json:"calls"`
	Complete bool              `json:"complete"`
	Status   string            `json:"status"`
}

func save(path string, v any) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(v)
}

// AuditFirst records a real saved API error without making another request.
func AuditFirst(body []byte, status int) Collection { return AuditScope(body, status, ApprovedScope()) }
func AuditScope(body []byte, status int, scope Scope) Collection {
	c := Collection{Scope: scope, Records: []json.RawMessage{}, Status: "failed_first_page"}
	p, e := Parse(body)
	l := PageLog{Page: 1, HTTP: status, Code: p.Code, Rows: len(p.Rows)}
	if e != nil {
		l.Error = e.Error()
		if errors.Is(e, ErrNoData) {
			c.Status = "no_data_for_requested_query"
		}
	} else if status == 200 {
		for _, raw := range p.Rows {
			var row map[string]json.RawMessage
			if json.Unmarshal(raw, &row) != nil || text(field(row, "opr_ymd")) != scope.Date || text(field(row, "rte_id")) != scope.Route {
				l.Error = "record date or route mismatch"
				c.Logs = []PageLog{l}
				return c
			}
		}
		if p.Number != 1 || p.Size != 1000 {
			l.Error = "pagination metadata mismatch"
			c.Logs = []PageLog{l}
			return c
		}
		n := p.Total
		pages := (n-1)/1000 + 1
		if n == 0 {
			pages = 1
		}
		c.Total = &n
		c.Pages = &pages
		c.Records = p.Rows
		c.Complete = pages == 1 && len(p.Rows) == n
		if c.Complete {
			c.Status = "complete"
		}
	}
	c.Logs = []PageLog{l}
	return c
}
func Collect(ctx context.Context, client *Client, dir string, maxPages int) (Collection, error) {
	return CollectScope(ctx, client, dir, maxPages, ApprovedScope())
}
func CollectScope(ctx context.Context, client *Client, dir string, maxPages int, scope Scope) (Collection, error) {
	c := Collection{Scope: scope, Records: []json.RawMessage{}, Status: "incomplete"}
	if !scope.Allowed() {
		return c, errors.New("query scope not authorized")
	}
	if maxPages < 1 || maxPages > 1000 {
		return c, errors.New("page budget must be 1..1000")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return c, e
	}
	// Never silently overwrite a prior run or repeat an ambiguous request.
	files, e := os.ReadDir(dir)
	if e != nil {
		return c, e
	}
	if len(files) > 0 {
		return c, errors.New("output directory must be empty; inspect existing checkpoint first")
	}
	fail := func(e error) (Collection, error) {
		c.Calls = client.Calls
		if len(c.Logs) > 0 && c.Logs[len(c.Logs)-1].Error == "" {
			c.Logs[len(c.Logs)-1].Error = e.Error()
		}
		_ = save(filepath.Join(dir, "collection.json"), c)
		return c, e
	}
	needed := 1
	for page := 1; page <= needed; page++ {
		b, status, e := client.Get(ctx, c.Scope, page)
		log := PageLog{Page: page, HTTP: status}
		if len(b) > 0 {
			ext := "json"
			if !json.Valid(b) {
				ext = "error.txt"
			}
			f, er := os.OpenFile(filepath.Join(dir, fmt.Sprintf("page-%04d.%s", page, ext)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if er != nil {
				return fail(er)
			}
			_, er = f.Write(b)
			closeErr := f.Close()
			if er != nil {
				return fail(er)
			}
			if closeErr != nil {
				return fail(closeErr)
			}
		}
		if e != nil {
			log.Error = e.Error()
			c.Logs = append(c.Logs, log)
			return fail(e)
		}
		p, e := Parse(b)
		log.Code = p.Code
		log.Rows = len(p.Rows)
		if e != nil {
			log.Error = e.Error()
			c.Logs = append(c.Logs, log)
			if errors.Is(e, ErrNoData) {
				c.Status = "no_data_for_requested_query"
			}
			return fail(e)
		}
		c.Logs = append(c.Logs, log)
		if page == 1 {
			n := p.Total
			c.Total = &n
			needed = (n-1)/1000 + 1
			if n == 0 {
				needed = 1
			}
			c.Pages = &needed
			if needed > maxPages {
				return fail(errors.New("required pages exceed explicit budget"))
			}
		}
		if p.Total != *c.Total || p.Number != page || p.Size != 1000 {
			return fail(errors.New("pagination metadata mismatch"))
		}
		expected := 1000
		if page == needed {
			expected = *c.Total - 1000*(page-1)
		}
		if len(p.Rows) != expected {
			return fail(errors.New("page row count mismatch"))
		}
		for _, raw := range p.Rows {
			var row map[string]json.RawMessage
			if json.Unmarshal(raw, &row) != nil || text(field(row, "opr_ymd")) != c.Scope.Date || text(field(row, "rte_id")) != c.Scope.Route {
				return fail(errors.New("record date or route mismatch"))
			}
		}
		c.Records = append(c.Records, p.Rows...)
	}
	c.Calls = client.Calls
	c.Complete = len(c.Records) == *c.Total
	c.Status = "complete"
	if e := save(filepath.Join(dir, "collection.json"), c); e != nil {
		return c, e
	}
	return c, nil
}
