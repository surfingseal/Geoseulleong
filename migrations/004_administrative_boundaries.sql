-- REVIEW ONLY. Separate approval required. No existing table/data changes.
BEGIN;
SET LOCAL lock_timeout='5s';
CREATE TABLE public.administrative_boundaries (
 boundary_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 provider text NOT NULL,
 code_system text NOT NULL,
 original_code text NOT NULL,
 province_name text NOT NULL,
 district_name text NOT NULL,
 reference_year integer NOT NULL CHECK(reference_year BETWEEN 1900 AND 2100),
 base_date date NOT NULL,
 source_version text NOT NULL,
 source_url text NOT NULL,
 source_sha256 text NOT NULL CHECK(source_sha256 ~ '^[0-9a-f]{64}$'),
 source_srid integer NOT NULL CHECK(source_srid=5179),
 geometry geometry(MultiPolygon,5179) NOT NULL,
 raw_feature jsonb NOT NULL CHECK(jsonb_typeof(raw_feature)='object'),
 collected_at timestamptz NOT NULL,
 CHECK(NOT ST_IsEmpty(geometry) AND ST_IsValid(geometry)),
 UNIQUE(provider,code_system,original_code,reference_year,source_version),
 UNIQUE(boundary_id,original_code,reference_year)
);
CREATE INDEX administrative_boundaries_gist ON public.administrative_boundaries USING gist(geometry);
CREATE TABLE public.administrative_code_mappings (
 mapping_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 boundary_id bigint NOT NULL,
 sgis_code text NOT NULL,
 boundary_year integer NOT NULL,
 code_reference_date date NOT NULL,
 stcis_province_code text,
 stcis_district_code text,
 congestion_ctpv_cd text,
 congestion_sgg_cd text,
 stcis_status text NOT NULL CHECK(stcis_status IN ('verified','candidate','mismatch','unresolved')),
 congestion_status text NOT NULL CHECK(congestion_status IN ('verified','candidate','mismatch','unresolved')),
 mapping_method text NOT NULL,
 evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='object'),
 verified_at timestamptz,
 FOREIGN KEY(boundary_id,sgis_code,boundary_year) REFERENCES public.administrative_boundaries(boundary_id,original_code,reference_year) ON DELETE RESTRICT,
 CHECK(stcis_province_code IS NULL OR stcis_province_code ~ '^[0-9]{2}$'),
 CHECK(stcis_district_code IS NULL OR stcis_district_code ~ '^[0-9]{5}$'),
 CHECK(congestion_ctpv_cd IS NULL OR congestion_ctpv_cd ~ '^[0-9]{2}$'),
 CHECK(congestion_sgg_cd IS NULL OR congestion_sgg_cd ~ '^[0-9]{5}$'),
 CHECK(stcis_status<>'verified' OR (stcis_province_code IS NOT NULL AND stcis_district_code IS NOT NULL AND evidence<>'{}'::jsonb AND verified_at IS NOT NULL)),
 CHECK(congestion_status<>'verified' OR (congestion_ctpv_cd IS NOT NULL AND congestion_sgg_cd IS NOT NULL AND evidence<>'{}'::jsonb AND verified_at IS NOT NULL)),
 UNIQUE(boundary_id,code_reference_date)
);
CREATE TABLE public.route_districts (
 route_district_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 analysis_id text NOT NULL CHECK(analysis_id ~ '^[0-9a-f]{64}$'),
 boundary_id bigint NOT NULL REFERENCES public.administrative_boundaries(boundary_id) ON DELETE RESTRICT,
 route_id bigint NOT NULL REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 shape_id bigint NOT NULL REFERENCES public.route_shapes(shape_id) ON DELETE RESTRICT,
 shape_fingerprint text NOT NULL,
 route_crs_status text NOT NULL,
 relation text NOT NULL CHECK(relation IN ('passes','short_overlap_review','boundary_contact','near_boundary_review')),
 intersection_length_m double precision NOT NULL CHECK(intersection_length_m>=0),
 interior_length_m double precision NOT NULL CHECK(interior_length_m>=0 AND interior_length_m<=intersection_length_m+0.001),
 passage_threshold_m double precision NOT NULL CHECK(passage_threshold_m>0),
 boundary_tolerance_m double precision NOT NULL CHECK(boundary_tolerance_m>=0),
 stop_count integer NOT NULL CHECK(stop_count>=0),
 confirmed_boarding_stop_count integer NOT NULL CHECK(confirmed_boarding_stop_count>=0),
 analysis_metadata jsonb NOT NULL,
 analyzed_at timestamptz NOT NULL,
 UNIQUE(analysis_id,boundary_id)
);
CREATE INDEX route_districts_route_idx ON public.route_districts(route_id,analysis_id);
CREATE VIEW public.qgis_route_districts AS
SELECT d.route_district_id,d.analysis_id,d.route_id,b.provider,b.original_code AS sgis_code,b.province_name,b.district_name,b.reference_year,b.base_date,d.relation,d.interior_length_m,d.stop_count,d.confirmed_boarding_stop_count,d.route_crs_status,ST_Transform(b.geometry,4326)::geometry(MultiPolygon,4326) AS geometry
FROM public.route_districts d JOIN public.administrative_boundaries b USING(boundary_id);
COMMIT;
