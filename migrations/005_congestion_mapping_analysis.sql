-- REVIEW ONLY. Requires 003 and 004. New objects only; no production application without approval.
BEGIN;
SET LOCAL lock_timeout='5s';
CREATE TABLE public.congestion_query_outcomes (
 query_id text PRIMARY KEY CHECK(query_id ~ '^[0-9a-f]{64}$'),
 service_date date NOT NULL, stcis_route_id text NOT NULL, ctpv_cd text NOT NULL, sgg_cd text NOT NULL,
 http_status integer NOT NULL, api_code text NOT NULL,
 status text NOT NULL CHECK(status IN ('no_data_for_query','error')),
 response_sha256 text NOT NULL CHECK(response_sha256 ~ '^[0-9a-f]{64}$'),
 raw_response jsonb NOT NULL, collected_at timestamptz NOT NULL,
 UNIQUE(query_id,service_date,stcis_route_id,ctpv_cd,sgg_cd)
);
CREATE TABLE public.congestion_analysis_sets (
 analysis_set_id text PRIMARY KEY CHECK(analysis_set_id ~ '^[0-9a-f]{64}$'),
 manifest jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
-- One explicitly selected snapshot per provider/date/route/region in this analysis set.
CREATE TABLE public.congestion_analysis_regions (
 region_selection_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 analysis_set_id text NOT NULL REFERENCES public.congestion_analysis_sets(analysis_set_id) ON DELETE RESTRICT,
 service_date date NOT NULL, stcis_route_id text NOT NULL, ctpv_cd text NOT NULL, sgg_cd text NOT NULL,
 batch_id text, query_id text,
 status text NOT NULL CHECK(status IN ('complete','no_data_for_query','error')),
 CHECK((status='complete' AND batch_id IS NOT NULL AND query_id IS NULL) OR (status<>'complete' AND batch_id IS NULL AND query_id IS NOT NULL)),
 FOREIGN KEY(batch_id,service_date,stcis_route_id,ctpv_cd,sgg_cd) REFERENCES public.congestion_batches(batch_id,service_date,stcis_route_id,ctpv_cd,sgg_cd) ON DELETE RESTRICT,
 FOREIGN KEY(query_id,service_date,stcis_route_id,ctpv_cd,sgg_cd) REFERENCES public.congestion_query_outcomes(query_id,service_date,stcis_route_id,ctpv_cd,sgg_cd) ON DELETE RESTRICT,
 UNIQUE(analysis_set_id,service_date,stcis_route_id,ctpv_cd,sgg_cd)
);
CREATE TABLE public.provider_stop_mappings (
 mapping_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 provider text NOT NULL, external_route_id text NOT NULL, external_stop_id text NOT NULL,
 source_sequence integer NOT NULL, service_date date NOT NULL,
 route_id bigint NOT NULL REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 route_stop_id bigint, stop_id varchar(50),
 direction_raw text, status text NOT NULL CHECK(status IN ('verified','candidate','ambiguous','unresolved','rejected')),
 method text NOT NULL, evidence jsonb NOT NULL, candidates jsonb NOT NULL DEFAULT '[]'::jsonb,
 distance_m double precision CHECK(distance_m IS NULL OR distance_m>=0),
 verified_at timestamptz,
 CHECK(jsonb_typeof(evidence)='object' AND jsonb_typeof(candidates)='array'),
 CHECK((status='verified' AND route_stop_id IS NOT NULL AND stop_id IS NOT NULL AND evidence<>'{}'::jsonb AND verified_at IS NOT NULL) OR (status<>'verified' AND route_stop_id IS NULL AND stop_id IS NULL)),
 FOREIGN KEY(route_stop_id,route_id,stop_id) REFERENCES public.route_stops(route_stop_id,route_id,stop_id) ON DELETE RESTRICT,
 UNIQUE(provider,external_route_id,external_stop_id,source_sequence,service_date)
);
CREATE INDEX provider_stop_mappings_route_idx ON public.provider_stop_mappings(route_id,status);
CREATE VIEW public.congestion_selected_observations AS
 SELECT r.analysis_set_id,o.* FROM public.congestion_analysis_regions r
 JOIN public.congestion_observations o ON o.batch_id=r.batch_id WHERE r.status='complete';
CREATE VIEW public.congestion_stop_time_profiles AS
 SELECT analysis_set_id,service_date,stcis_route_id,stcis_stop_id,stop_sequence,time_code,
 count(*) AS observation_count,count(cgst_raw) AS numeric_count,
 min(cgst_raw) AS min_cgst,max(cgst_raw) AS max_cgst,avg(cgst_raw) AS mean_percent_hypothesis,
 percentile_cont(ARRAY[0.5,0.75,0.9,0.95]) WITHIN GROUP(ORDER BY cgst_raw::double precision) AS quantiles_r7,
 'cgst-percent-v1'::text AS policy_version,'hypothesis_unverified'::text AS policy_status
 FROM public.congestion_selected_observations GROUP BY analysis_set_id,service_date,stcis_route_id,stcis_stop_id,stop_sequence,time_code;
-- Only verified visit mappings receive geometry. Every unresolved source stop stays visible without geometry.
CREATE VIEW public.qgis_congestion_stops AS
 SELECT row_number() OVER(ORDER BY a.analysis_set_id,a.service_date,a.stcis_route_id,a.stcis_stop_id,a.stop_sequence) AS feature_id,
 a.*,m.mapping_id,coalesce(m.status,'unresolved') AS mapping_status,m.route_stop_id,s.stop_name,s.stop_crs_status AS crs_status,s.boarding_allowed,
 s.location::geometry(Point,4326) AS geometry
 FROM (
 SELECT analysis_set_id,service_date,stcis_route_id,stcis_stop_id,stop_sequence,count(*) AS observation_count,
 avg(cgst_raw) AS mean_percent_hypothesis,min(cgst_raw) AS min_cgst,max(cgst_raw) AS max_cgst,
 percentile_cont(0.90) WITHIN GROUP(ORDER BY cgst_raw::double precision) AS p90_percent_hypothesis,
 percentile_cont(0.95) WITHIN GROUP(ORDER BY cgst_raw::double precision) AS p95_percent_hypothesis,
 'hypothesis_unverified'::text AS policy_status
 FROM public.congestion_selected_observations GROUP BY analysis_set_id,service_date,stcis_route_id,stcis_stop_id,stop_sequence) a
 LEFT JOIN public.provider_stop_mappings m ON m.provider='stcis' AND m.external_route_id=a.stcis_route_id AND m.external_stop_id=a.stcis_stop_id AND m.source_sequence=a.stop_sequence AND m.service_date=a.service_date
 LEFT JOIN public.qgis_route_stops_current s ON m.status='verified' AND s.stop_id=m.stop_id AND s.route_stop_id=m.route_stop_id;
CREATE VIEW public.qgis_congestion_districts AS
 SELECT r.region_selection_id,r.analysis_set_id,r.service_date,r.stcis_route_id,r.ctpv_cd,r.sgg_cd,r.status,
 b.total_count,q.api_code,(r.status='no_data_for_query') AS data_not_provided,
 (SELECT avg(o.cgst_raw) FROM public.congestion_observations o WHERE o.batch_id=r.batch_id) AS mean_percent_hypothesis,
 'hypothesis_unverified'::text AS policy_status,
 geom.province_name,geom.district_name,geom.reference_year,
 ST_Transform(geom.geometry,4326)::geometry(MultiPolygon,4326) AS geometry
 FROM public.congestion_analysis_regions r
 LEFT JOIN public.congestion_batches b USING(batch_id)
 LEFT JOIN public.congestion_query_outcomes q USING(query_id)
 LEFT JOIN LATERAL (
 SELECT ab.* FROM public.administrative_code_mappings cm JOIN public.administrative_boundaries ab USING(boundary_id)
 WHERE cm.congestion_ctpv_cd=r.ctpv_cd AND cm.congestion_sgg_cd=r.sgg_cd AND cm.congestion_status='verified'
 AND cm.code_reference_date<=r.service_date ORDER BY cm.code_reference_date DESC,ab.base_date DESC LIMIT 1
 ) geom ON true;
COMMIT;
