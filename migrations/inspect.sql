-- Run BEFORE application. All queries are read-only.
BEGIN READ ONLY;
SELECT current_database(), current_setting('server_version');
SELECT extname,extversion FROM pg_extension;
SELECT column_name,data_type,udt_name,character_maximum_length,is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='bus_stops' ORDER BY ordinal_position;
SELECT indexname,indexdef FROM pg_indexes WHERE schemaname='public' AND tablename='bus_stops';
SELECT count(*) AS stops, count(location) AS located_stops FROM public.bus_stops;
SELECT relname FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind IN ('r','p') ORDER BY relname;
COMMIT;
