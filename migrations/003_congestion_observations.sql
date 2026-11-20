-- REVIEW ONLY: production application requires separate user approval.
-- Creates 3 new tables; never ALTER/DROP/UPDATE existing transport tables.
BEGIN;
SET LOCAL lock_timeout='5s';
CREATE TABLE public.congestion_batches (
 batch_id text PRIMARY KEY CHECK(batch_id ~ '^[0-9a-f]{64}$'),
 source text NOT NULL CHECK(source='molit_route_congestion'),
 service_date date NOT NULL,
 stcis_route_id text NOT NULL,
 ctpv_cd text NOT NULL,
 sgg_cd text NOT NULL,
 internal_route_id bigint REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 total_count integer NOT NULL CHECK(total_count>=0),
 page_count integer NOT NULL CHECK(page_count>0),
 interpretation_version text NOT NULL,
 interpretation_policy jsonb NOT NULL CHECK(jsonb_typeof(interpretation_policy)='object' AND interpretation_policy ? 'status' AND interpretation_policy ? 'version' AND interpretation_policy->>'status'='hypothesis_unverified' AND interpretation_policy->>'version'=interpretation_version),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(batch_id,service_date,stcis_route_id,ctpv_cd,sgg_cd)
);
CREATE INDEX congestion_batches_scope_idx ON public.congestion_batches(stcis_route_id,service_date,ctpv_cd,sgg_cd);
CREATE TABLE public.congestion_pages (
 batch_id text NOT NULL REFERENCES public.congestion_batches(batch_id) ON DELETE RESTRICT,
 page_no integer NOT NULL CHECK(page_no>0),
 http_status integer NOT NULL CHECK(http_status=200),
 record_count integer NOT NULL CHECK(record_count>=0),
 response_sha256 text NOT NULL CHECK(response_sha256 ~ '^[0-9a-f]{64}$'),
 collected_at timestamptz NOT NULL,
 raw_response jsonb NOT NULL,
 PRIMARY KEY(batch_id,page_no)
);
CREATE TABLE public.congestion_observations (
 batch_id text NOT NULL,
 page_no integer NOT NULL,
 row_no integer NOT NULL CHECK(row_no>0),
 service_date date NOT NULL,
 stcis_route_id text NOT NULL,
 ctpv_cd text NOT NULL,
 sgg_cd text NOT NULL,
 stcis_stop_id text,
 stop_sequence integer,
 trip_ordinal text,
 time_code text,
 cgst_raw numeric,
 raw_record jsonb NOT NULL CHECK(jsonb_typeof(raw_record)='object'),
 PRIMARY KEY(batch_id,page_no,row_no),
 FOREIGN KEY(batch_id,page_no) REFERENCES public.congestion_pages(batch_id,page_no) ON DELETE RESTRICT,
 FOREIGN KEY(batch_id,service_date,stcis_route_id,ctpv_cd,sgg_cd) REFERENCES public.congestion_batches(batch_id,service_date,stcis_route_id,ctpv_cd,sgg_cd) ON DELETE RESTRICT
);
-- No UNIQUE on station/time/trip or raw hash: repeated identical observations survive.
CREATE INDEX congestion_observations_route_time_idx ON public.congestion_observations(stcis_route_id,service_date,time_code);
CREATE INDEX congestion_observations_stop_time_idx ON public.congestion_observations(stcis_stop_id,service_date,time_code);
CREATE INDEX congestion_observations_date_brin_idx ON public.congestion_observations USING brin(service_date);
COMMIT;
