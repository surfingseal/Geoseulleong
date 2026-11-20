-- REVIEW ONLY: explicit approval required before applying to geoseulleong.
-- Additive migration; existing public.bus_stops is never altered.
BEGIN;
SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '60s';
SELECT pg_advisory_xact_lock(hashtext('geoseulleong_schema_001'));
DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname='postgis') THEN
   RAISE EXCEPTION 'PostGIS must already be installed; no extension is installed automatically';
 END IF;
 IF to_regclass('public.bus_stops') IS NULL THEN RAISE EXCEPTION 'existing public.bus_stops missing'; END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='public.bus_stops'::regclass AND attname='stop_id' AND format_type(atttypid,atttypmod)='character varying(50)' AND attnotnull) THEN
   RAISE EXCEPTION 'bus_stops.stop_id differs from reviewed varchar(50) NOT NULL';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.bus_stops'::regclass AND contype='p' AND pg_get_constraintdef(oid)='PRIMARY KEY (stop_id)') THEN
   RAISE EXCEPTION 'bus_stops primary key differs from reviewed schema';
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='public.bus_stops'::regclass AND attname='location' AND format_type(atttypid,atttypmod)='geometry(Point,4326)') THEN
   RAISE EXCEPTION 'bus_stops.location differs from Point/4326';
 END IF;
END $$;
-- Plain CREATE intentionally fails on any preexisting target object, rolling back.
CREATE TABLE public.data_sources (
 source_id text PRIMARY KEY CHECK (btrim(source_id)<>''),
 name text NOT NULL,
 documentation_url text,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE public.bus_routes (
 route_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 route_number text NOT NULL CHECK (btrim(route_number)<>''),
 operator_name text,
 route_type_raw text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bus_routes_number_idx ON public.bus_routes(route_number);
-- Internal stop_id remains opaque; external IDs are not assigned a common namespace.
CREATE TABLE public.route_external_ids (
 source_id text NOT NULL REFERENCES public.data_sources(source_id) ON DELETE RESTRICT,
 external_route_id text NOT NULL CHECK (btrim(external_route_id)<>''),
 route_id bigint NOT NULL REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 evidence text NOT NULL CHECK (btrim(evidence)<>''),
 verified_at timestamptz NOT NULL,
 PRIMARY KEY(source_id,external_route_id)
);
CREATE INDEX route_external_ids_route_idx ON public.route_external_ids(route_id);
CREATE TABLE public.stop_external_ids (
 source_id text NOT NULL REFERENCES public.data_sources(source_id) ON DELETE RESTRICT,
 external_stop_id text NOT NULL CHECK (btrim(external_stop_id)<>''),
 stop_id varchar(50) NOT NULL REFERENCES public.bus_stops(stop_id) ON DELETE RESTRICT,
 evidence text NOT NULL CHECK (btrim(evidence)<>''),
 verified_at timestamptz NOT NULL,
 PRIMARY KEY(source_id,external_stop_id)
);
CREATE INDEX stop_external_ids_stop_idx ON public.stop_external_ids(stop_id);
-- A pattern is a verified ordering/version, not an inferred direction code.
CREATE TABLE public.route_patterns (
 pattern_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 route_id bigint NOT NULL REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 source_id text NOT NULL REFERENCES public.data_sources(source_id) ON DELETE RESTRICT,
 pattern_key text NOT NULL CHECK (btrim(pattern_key)<>''),
 version_key text NOT NULL CHECK (btrim(version_key)<>''),
 direction_raw text,
 valid_from date,
 valid_to date,
 observed_at timestamptz NOT NULL,
 evidence text NOT NULL CHECK (btrim(evidence)<>''),
 CHECK(valid_to IS NULL OR valid_from IS NULL OR valid_to>=valid_from),
 UNIQUE(route_id,source_id,pattern_key,version_key),
 UNIQUE(pattern_id,route_id)
);
CREATE INDEX route_patterns_source_idx ON public.route_patterns(source_id);
CREATE TABLE public.route_stops (
 route_stop_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 pattern_id bigint NOT NULL,
 route_id bigint NOT NULL,
 stop_id varchar(50) NOT NULL REFERENCES public.bus_stops(stop_id) ON DELETE RESTRICT,
 stop_sequence integer NOT NULL CHECK(stop_sequence>0),
 is_turnaround boolean,
 FOREIGN KEY(pattern_id,route_id) REFERENCES public.route_patterns(pattern_id,route_id) ON DELETE RESTRICT,
 UNIQUE(pattern_id,stop_sequence),
 UNIQUE(route_stop_id,route_id,stop_id)
);
-- Repeated visits to the same stop are allowed at different sequences.
CREATE INDEX route_stops_stop_idx ON public.route_stops(stop_id,route_id);
CREATE INDEX route_stops_route_idx ON public.route_stops(route_id,pattern_id,stop_sequence);
CREATE TABLE public.route_shapes (
 shape_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 pattern_id bigint NOT NULL,
 route_id bigint NOT NULL,
 source_id text NOT NULL REFERENCES public.data_sources(source_id) ON DELETE RESTRICT,
 version_key text NOT NULL,
 shape geometry(MultiLineString,4326) NOT NULL,
 observed_at timestamptz NOT NULL,
 raw_metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(raw_metadata)='object'),
 FOREIGN KEY(pattern_id,route_id) REFERENCES public.route_patterns(pattern_id,route_id) ON DELETE RESTRICT,
 CHECK(NOT ST_IsEmpty(shape) AND ST_IsValid(shape)),
 UNIQUE(pattern_id,source_id,version_key)
);
CREATE INDEX route_shapes_geom_idx ON public.route_shapes USING gist(shape);
CREATE INDEX route_shapes_route_idx ON public.route_shapes(route_id,pattern_id);
CREATE INDEX route_shapes_source_idx ON public.route_shapes(source_id);
CREATE TABLE public.ingest_batches (
 batch_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 source_id text NOT NULL REFERENCES public.data_sources(source_id) ON DELETE RESTRICT,
 dataset_key text NOT NULL,
 request_fingerprint text NOT NULL CHECK(request_fingerprint ~ '^[0-9a-f]{64}$'),
 response_sha256 text NOT NULL CHECK(response_sha256 ~ '^[0-9a-f]{64}$'),
 retrieved_at timestamptz NOT NULL,
 request_parameters jsonb NOT NULL CHECK(jsonb_typeof(request_parameters)='object'),
 raw_response jsonb NOT NULL,
 UNIQUE(source_id,dataset_key,request_fingerprint,response_sha256),
 UNIQUE(batch_id,source_id)
);
-- NEVER put serviceKey, passwords, or authenticated request URLs in JSON.
CREATE TABLE public.boarding_stats (
 stat_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 batch_id bigint NOT NULL,
 source_id text NOT NULL,
 source_row_number integer NOT NULL CHECK(source_row_number>0),
 source_row_sha256 text NOT NULL CHECK(source_row_sha256 ~ '^[0-9a-f]{64}$'),
 service_date date,
 time_slot_raw text,
 time_start_minutes integer CHECK(time_start_minutes>=0),
 time_end_minutes integer,
 time_definition text,
 timezone text NOT NULL DEFAULT 'Asia/Seoul' CHECK(timezone='Asia/Seoul'),
 external_route_id text,
 route_number_raw text,
 external_stop_id text,
 stop_name_raw text,
 direction_raw text,
 stop_sequence_raw integer,
 route_id bigint REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 stop_id varchar(50) REFERENCES public.bus_stops(stop_id) ON DELETE RESTRICT,
 route_stop_id bigint,
 raw_record jsonb NOT NULL CHECK(jsonb_typeof(raw_record)='object'),
 recorded_at timestamptz NOT NULL DEFAULT now(),
 boarding_count bigint CHECK(boarding_count>=0),
 alighting_count bigint CHECK(alighting_count>=0),
 trip_count bigint CHECK(trip_count>=0),

 FOREIGN KEY(batch_id,source_id) REFERENCES public.ingest_batches(batch_id,source_id) ON DELETE RESTRICT,
 FOREIGN KEY(route_stop_id,route_id,stop_id) REFERENCES public.route_stops(route_stop_id,route_id,stop_id) ON DELETE RESTRICT,
 CHECK(route_stop_id IS NULL OR (route_id IS NOT NULL AND stop_id IS NOT NULL)),
 CHECK(time_end_minutes IS NULL OR (time_start_minutes IS NOT NULL AND time_end_minutes>time_start_minutes)),
 CHECK(time_start_minutes IS NULL OR NULLIF(btrim(time_definition),'') IS NOT NULL),
 UNIQUE(batch_id,source_row_number)
);
CREATE INDEX boarding_stats_route_date_time_idx ON public.boarding_stats(route_id,service_date,time_slot_raw);
CREATE INDEX boarding_stats_stop_date_idx ON public.boarding_stats(stop_id,service_date);
CREATE INDEX boarding_stats_route_stop_idx ON public.boarding_stats(route_stop_id) WHERE route_stop_id IS NOT NULL;
CREATE INDEX boarding_stats_source_external_idx ON public.boarding_stats(source_id,external_route_id,service_date);
CREATE INDEX boarding_stats_date_brin_idx ON public.boarding_stats USING brin(service_date);
CREATE TABLE public.congestion_stats (
 stat_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 batch_id bigint NOT NULL,
 source_id text NOT NULL,
 source_row_number integer NOT NULL CHECK(source_row_number>0),
 source_row_sha256 text NOT NULL CHECK(source_row_sha256 ~ '^[0-9a-f]{64}$'),
 service_date date,
 time_slot_raw text,
 time_start_minutes integer CHECK(time_start_minutes>=0),
 time_end_minutes integer,
 time_definition text,
 timezone text NOT NULL DEFAULT 'Asia/Seoul' CHECK(timezone='Asia/Seoul'),
 external_route_id text,
 route_number_raw text,
 external_stop_id text,
 stop_name_raw text,
 direction_raw text,
 stop_sequence_raw integer,
 route_id bigint REFERENCES public.bus_routes(route_id) ON DELETE RESTRICT,
 stop_id varchar(50) REFERENCES public.bus_stops(stop_id) ON DELETE RESTRICT,
 route_stop_id bigint,
 raw_record jsonb NOT NULL CHECK(jsonb_typeof(raw_record)='object'),
 recorded_at timestamptz NOT NULL DEFAULT now(),
 congestion_value numeric,
 congestion_unit text,
 metric_definition text,
 trip_ordinal_raw text,
 region_codes_raw jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(region_codes_raw)='object'),

 FOREIGN KEY(batch_id,source_id) REFERENCES public.ingest_batches(batch_id,source_id) ON DELETE RESTRICT,
 FOREIGN KEY(route_stop_id,route_id,stop_id) REFERENCES public.route_stops(route_stop_id,route_id,stop_id) ON DELETE RESTRICT,
 CHECK(route_stop_id IS NULL OR (route_id IS NOT NULL AND stop_id IS NOT NULL)),
 CHECK(time_end_minutes IS NULL OR (time_start_minutes IS NOT NULL AND time_end_minutes>time_start_minutes)),
 CHECK(time_start_minutes IS NULL OR NULLIF(btrim(time_definition),'') IS NOT NULL),
 UNIQUE(batch_id,source_row_number)
);
CREATE INDEX congestion_stats_route_date_time_idx ON public.congestion_stats(route_id,service_date,time_slot_raw);
CREATE INDEX congestion_stats_stop_date_idx ON public.congestion_stats(stop_id,service_date);
CREATE INDEX congestion_stats_route_stop_idx ON public.congestion_stats(route_stop_id) WHERE route_stop_id IS NOT NULL;
CREATE INDEX congestion_stats_source_external_idx ON public.congestion_stats(source_id,external_route_id,service_date);
CREATE INDEX congestion_stats_date_brin_idx ON public.congestion_stats USING brin(service_date);
COMMIT;
