package collectionrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"geoseulleong/internal/collectionpreview"
	cp "geoseulleong/internal/congestionpipeline"
	rc "geoseulleong/internal/routecongestion"
	"os"
	"path/filepath"
)

// Preview inspects saved pages without creating files or opening network/DB connections.
// The executor revalidates them under the global lock; this output is not a lease.
func (r Runner) Preview() (collectionpreview.Preview, error) {
	if r.Permit == nil {
		return collectionpreview.Preview{}, errors.New("verified permit required")
	}
	inventories := []collectionpreview.Snapshot{}
	for _, task := range r.Permit.Preview.Tasks {
		s := task.Scope
		dir := filepath.Join(r.Directory, Digest([]byte(collectionpreview.ScopeKey(s))))
		inv := collectionpreview.Snapshot{Scope: s, ID: Digest([]byte(collectionpreview.ScopeKey(s))), State: "partial"}
		pages := []cp.Page{}
		needed := 1
		for n := 1; n <= needed; n++ {
			var c Checkpoint
			e := read(filepath.Join(dir, fmt.Sprintf("page-%04d.checkpoint.json", n)), &c)
			if os.IsNotExist(e) {
				break
			}
			if e != nil {
				return collectionpreview.Preview{}, e
			}
			if c.Scope != s || c.Page != n || c.Plan != r.Permit.Hash {
				return collectionpreview.Preview{}, errors.New("checkpoint binding conflict")
			}
			if c.Status != "success" && c.Status != "no_data_for_query" {
				inv.State = c.Status
				if inv.State != "pending" && inv.State != "failed" && inv.State != "ambiguous" {
					return collectionpreview.Preview{}, errors.New("unknown checkpoint state")
				}
				break
			}
			if c.SHA != Digest(c.Raw) || c.At.IsZero() {
				return collectionpreview.Preview{}, errors.New("checkpoint hash/timestamp conflict")
			}
			if c.Status == "no_data_for_query" {
				parsed, pe := rc.Parse(c.Raw)
				if n != 1 || c.HTTP != 200 || !errors.Is(pe, rc.ErrNoData) || parsed.Code != "50" {
					return collectionpreview.Preview{}, errors.New("invalid code50 checkpoint")
				}
				inv.State = "no_data_for_query"
				b, _ := json.Marshal(s)
				inv.ID = Digest(append(b, []byte(c.SHA)...))
				break
			}
			parsed, e := cp.CheckAuthorized(c.Raw, c.HTTP, s, n, r.Permit.Allows)
			if e != nil {
				return collectionpreview.Preview{}, e
			}
			if inv.Total == nil {
				total := int64(parsed.Total)
				inv.Total = &total
				needed = max(1, (parsed.Total+999)/1000)
			} else if *inv.Total != int64(parsed.Total) {
				return collectionpreview.Preview{}, errors.New("totalCount conflict")
			}
			inv.Pages = append(inv.Pages, collectionpreview.SavedPage{Number: int64(n), Rows: int64(len(parsed.Rows)), SHA: c.SHA})
			pages = append(pages, cp.Page{Number: n, HTTP: c.HTTP, Raw: c.Raw, SHA: c.SHA, At: c.At})
			if needed > int(r.Permit.Config.Manifest.Budget.MaxPages) {
				break
			}
		}
		if inv.State == "partial" && inv.Total != nil && len(pages) == needed {
			b, e := cp.BuildAuthorized(s, pages, r.Permit.Allows)
			if e != nil {
				return collectionpreview.Preview{}, e
			}
			inv.State = "complete"
			inv.ID = b.ID
		}
		if len(inv.Pages) > 0 || inv.State != "partial" {
			inventories = append(inventories, inv)
		}
	}
	return collectionpreview.Build(r.Permit.Config.Manifest, inventories)
}
