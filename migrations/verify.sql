-- Read-only verification AFTER an approved application.
BEGIN READ ONLY;
DO $$
DECLARE t text;
BEGIN
 FOREACH t IN ARRAY ARRAY['bus_stops','bus_routes','route_patterns','route_stops','route_shapes','data_sources','route_external_ids','stop_external_ids','ingest_batches','boarding_stats','congestion_stats'] LOOP
  IF to_regclass('public.'||t) IS NULL THEN RAISE EXCEPTION 'missing table %', t; END IF;
 END LOOP;
 IF NOT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='route_shapes' AND indexdef LIKE '%USING gist%') THEN RAISE EXCEPTION 'shape GiST missing'; END IF;
 IF EXISTS(SELECT 1 FROM pg_constraint WHERE connamespace='public'::regnamespace AND NOT convalidated) THEN RAISE EXCEPTION 'unvalidated constraint'; END IF;
END $$;
SELECT table_name FROM information_schema.tables WHERE table_schema='public' ORDER BY 1;
SELECT count(*) AS bus_stops_count FROM public.bus_stops;
COMMIT;
