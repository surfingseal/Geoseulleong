// Package repository contains parameterized read-only SQL; no HTTP or collectors.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

var ErrNotFound = errors.New("route not found")
var ErrAmbiguous = errors.New("route name is ambiguous")
var ErrQuery = errors.New("database query failed")
var ErrInvalidData = errors.New("stored route data invalid")

type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type Routes struct{ db DB }

func NewRoutes(db DB) *Routes { return &Routes{db: db} }

type ExternalID struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}
type Route struct {
	ID          int64        `json:"route_id"`
	Name        string       `json:"route_name"`
	TypeCode    *string      `json:"route_type_cd"`
	TypeName    *string      `json:"route_type_name"`
	Region      *string      `json:"region_name"`
	Admin       *string      `json:"admin_name"`
	ExternalIDs []ExternalID `json:"external_ids"`
}
type Stop struct {
	VisitID         int64           `json:"visit_id"`
	ID              string          `json:"stop_id"`
	Name            string          `json:"stop_name"`
	Sequence        int             `json:"stop_sequence"`
	Turn            *string         `json:"turn_yn"`
	TurnSequence    *int            `json:"turn_seq"`
	ViaName         bool            `json:"is_via_name"`
	BoardingAllowed *bool           `json:"boarding_allowed"`
	MobileNo        *string         `json:"mobile_no"`
	Location        json.RawMessage `json:"location"`
	CRSStatus       string          `json:"crs_status"`
}

func bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 3*time.Second)
}

// RouteByID accepts ONLY the internal route_id, never an unverified external ID.
func (r *Routes) RouteByID(ctx context.Context, id int64) (Route, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	var b []byte
	e := r.db.QueryRow(ctx, routeByIDSQL, id).Scan(&b)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return Route{}, ErrNotFound
		}
		return Route{}, ErrQuery
	}
	var route Route
	if json.Unmarshal(b, &route) != nil {
		return route, ErrInvalidData
	}
	return route, nil
}
func (r *Routes) RouteByName(ctx context.Context, name string) (Route, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	rows, e := r.db.Query(ctx, routeByNameSQL, name)
	if e != nil {
		return Route{}, ErrQuery
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil {
			return Route{}, ErrQuery
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return Route{}, ErrQuery
	}
	rows.Close()
	if len(ids) == 0 {
		return Route{}, ErrNotFound
	}
	if len(ids) > 1 {
		return Route{}, ErrAmbiguous
	}
	return r.RouteByID(ctx, ids[0])
}

// ResolveExternalID explicitly separates provider namespace from internal PK.
func (r *Routes) ResolveExternalID(ctx context.Context, source, external string) (Route, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	var id int64
	e := r.db.QueryRow(ctx, externalIDSQL, source, external).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return Route{}, ErrNotFound
	}
	if e != nil {
		return Route{}, ErrQuery
	}
	return r.RouteByID(ctx, id)
}
func (r *Routes) Stops(ctx context.Context, id int64) ([]Stop, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	rows, e := r.db.Query(ctx, stopsSQL, id)
	if e != nil {
		return nil, ErrQuery
	}
	defer rows.Close()
	out := []Stop{}
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) != nil {
			return nil, ErrQuery
		}
		var s Stop
		if json.Unmarshal(b, &s) != nil {
			return nil, ErrInvalidData
		}
		out = append(out, s)
	}
	if rows.Err() != nil {
		return nil, ErrQuery
	}
	return out, nil
}
func (r *Routes) Shape(ctx context.Context, id int64) (json.RawMessage, error) {
	ctx, cancel := bound(ctx)
	defer cancel()
	var b []byte
	e := r.db.QueryRow(ctx, shapeSQL, id).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, ErrQuery
	}
	if !json.Valid(b) {
		return nil, ErrInvalidData
	}
	return json.RawMessage(b), nil
}

const routeByNameSQL = `SELECT route_id FROM public.bus_routes WHERE route_number=$1 ORDER BY route_id LIMIT 2`
const externalIDSQL = `SELECT route_id FROM public.route_external_ids WHERE source_id=$1 AND external_route_id=$2`
const routeByIDSQL = `SELECT jsonb_build_object('route_id',r.route_id,'route_name',r.route_number,'route_type_cd',r.route_type_raw,'route_type_name',r.route_type_name,'region_name',r.region_name,'admin_name',r.admin_name,'external_ids',COALESCE((SELECT jsonb_agg(jsonb_build_object('source',e.source_id,'id',e.external_route_id) ORDER BY e.source_id,e.external_route_id) FROM public.route_external_ids e WHERE e.route_id=r.route_id),'[]'::jsonb)) FROM public.bus_routes r WHERE r.route_id=$1`
const stopsSQL = `SELECT jsonb_build_object('visit_id',route_stop_id,'stop_id',stop_id,'stop_name',stop_name,'stop_sequence',stop_sequence,'turn_yn',turn_yn,'turn_seq',turn_seq,'is_via_name',is_via_name,'boarding_allowed',boarding_allowed,'mobile_no',mobile_no,'location',ST_AsGeoJSON(location,15,0)::jsonb,'crs_status',stop_crs_status) FROM public.qgis_route_stops_current WHERE route_id=$1 ORDER BY stop_sequence,route_stop_id`
const shapeSQL = `SELECT jsonb_build_object('type','Feature','id',shape_id,'geometry',ST_AsGeoJSON(geometry,15,0)::jsonb,'properties',jsonb_build_object('route_id',route_id,'gbis_route_id',gbis_route_id,'crs_status',crs_status,'officially_verified',crs_status='verified','point_count',ST_NPoints(geometry))) FROM public.qgis_route_shapes_current WHERE route_id=$1 AND geometry IS NOT NULL ORDER BY observed_at DESC,shape_id DESC LIMIT 1`
