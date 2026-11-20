-- Explicitly approved hypothesis application, not official CRS verification.
-- One-time migration and data patch. No public API calls.
BEGIN;
SET LOCAL lock_timeout='5s';
SET LOCAL statement_timeout='60s';
SELECT pg_advisory_xact_lock(hashtext('geoseulleong-crs-apply'));
LOCK TABLE public.bus_stops,public.route_shapes,public.route_shape_points,public.route_stops,public.route_imports,public.bus_routes,public.route_external_ids IN SHARE ROW EXCLUSIVE MODE;
DO $$ BEGIN
 IF md5((jsonb_build_object('stops',(SELECT jsonb_agg(to_jsonb(s) ORDER BY stop_id) FROM bus_stops s),'shapes',(SELECT jsonb_agg(to_jsonb(s) ORDER BY shape_id) FROM route_shapes s),'points',(SELECT jsonb_agg(to_jsonb(s) ORDER BY shape_id,line_seq) FROM route_shape_points s),'visits',(SELECT jsonb_agg(to_jsonb(s) ORDER BY route_stop_id) FROM route_stops s),'imports',(SELECT jsonb_agg(to_jsonb(s) ORDER BY input_hash) FROM route_imports s)))::text)<>'cf7ca87f808e978e9828406bbbcd0a14' THEN RAISE EXCEPTION 'Source state changed: abort before DDL'; END IF;
 IF NOT EXISTS(SELECT 1 FROM route_imports i JOIN route_external_ids e USING(route_id) WHERE i.input_hash='2ec30ba55009ce71cd085f208cb84e278ddcea6645b6934b76fcaaefa559862c' AND e.source_id='gbis' AND e.external_route_id='234001317' AND i.crs_status='unverified') THEN RAISE EXCEPTION 'Target input/mapping/status mismatch'; END IF;
END $$;
ALTER TABLE public.bus_stops
 ADD COLUMN crs_status text NOT NULL DEFAULT 'unverified',
 ADD COLUMN crs_evidence text,
 ADD CONSTRAINT bus_stops_crs_status_check CHECK(crs_status IN ('unverified','hypothesis_validated','verified')),
 ADD CONSTRAINT bus_stops_crs_geometry_check CHECK(location IS NULL OR (crs_status IN ('hypothesis_validated','verified') AND NULLIF(btrim(crs_evidence),'') IS NOT NULL AND ST_IsValid(location)));
ALTER TABLE public.route_shapes DROP CONSTRAINT route_shapes_crs_status_check;
ALTER TABLE public.route_shapes DROP CONSTRAINT route_shapes_check;
ALTER TABLE public.route_shapes
 ADD CONSTRAINT route_shapes_crs_status_check CHECK(crs_status IN ('unverified','hypothesis_validated','verified')),
 ADD CONSTRAINT route_shapes_check CHECK(geometry IS NULL OR (crs_status IN ('hypothesis_validated','verified') AND NULLIF(btrim(crs_evidence),'') IS NOT NULL AND NOT ST_IsEmpty(geometry) AND ST_IsValid(geometry)));
DO $$
DECLARE pid bigint; rid bigint; sid bigint; changed bigint;
BEGIN
 SELECT pattern_id,route_id INTO STRICT pid,rid FROM route_imports WHERE input_hash='2ec30ba55009ce71cd085f208cb84e278ddcea6645b6934b76fcaaefa559862c';
 SELECT shape_id INTO STRICT sid FROM route_shapes WHERE pattern_id=pid AND route_id=rid AND geometry IS NULL AND crs_status='unverified';
 IF (SELECT count(*) FROM route_stops WHERE pattern_id=pid)<>60 OR (SELECT count(*) FROM route_shape_points WHERE shape_id=sid)<>868 THEN RAISE EXCEPTION 'Unexpected source counts'; END IF;
 IF EXISTS(SELECT 1 FROM (SELECT stop_id,count(DISTINCT (raw_record->>'x',raw_record->>'y')) AS n FROM route_stops WHERE pattern_id=pid GROUP BY stop_id) c WHERE c.n<>1) THEN RAISE EXCEPTION 'Conflicting coordinates for a stop'; END IF;
 UPDATE public.bus_stops b SET location=ST_SetSRID(ST_MakePoint((r.raw_record->>'x')::float8,(r.raw_record->>'y')::float8),4326),crs_status='hypothesis_validated',crs_evidence='{"status": "hypothesis_validated", "assumed_crs": "EPSG:4326", "officially_verified": false, "method": "isolated_PostGIS_full_coordinate_comparison", "validation_sha256": "652fcce28c9d68028424ceacbff0db94b91649f73417b52ce8d71cca18f8eb4a", "qgis_visual_inspection_performed": false, "date": "2026-11-12", "official_document_scope": "Target APIs describe x/y but direct CRS not confirmed"}'
 FROM route_stops r WHERE r.pattern_id=pid AND r.stop_id=b.stop_id AND b.location IS NULL AND b.crs_status='unverified';
 GET DIAGNOSTICS changed=ROW_COUNT; IF changed<>60 THEN RAISE EXCEPTION 'Expected 60 NULL stop locations, got %',changed; END IF;
 UPDATE public.route_shapes SET geometry=(SELECT ST_MakeLine(ST_SetSRID(ST_MakePoint(x,y),4326) ORDER BY line_seq) FROM route_shape_points WHERE shape_id=sid),crs_status='hypothesis_validated',crs_evidence='{"status": "hypothesis_validated", "assumed_crs": "EPSG:4326", "officially_verified": false, "method": "isolated_PostGIS_full_coordinate_comparison", "validation_sha256": "652fcce28c9d68028424ceacbff0db94b91649f73417b52ce8d71cca18f8eb4a", "qgis_visual_inspection_performed": false, "date": "2026-11-12", "official_document_scope": "Target APIs describe x/y but direct CRS not confirmed"}' WHERE shape_id=sid AND geometry IS NULL AND crs_status='unverified';
 GET DIAGNOSTICS changed=ROW_COUNT; IF changed<>1 THEN RAISE EXCEPTION 'Expected one shape update'; END IF;
 UPDATE public.route_imports SET crs_status='hypothesis_validated' WHERE input_hash='2ec30ba55009ce71cd085f208cb84e278ddcea6645b6934b76fcaaefa559862c' AND crs_status='unverified';
 IF EXISTS(SELECT 1 FROM route_stops r JOIN bus_stops b USING(stop_id) WHERE r.pattern_id=pid AND (ST_SRID(b.location)<>4326 OR ST_X(b.location)<>(r.raw_record->>'x')::float8 OR ST_Y(b.location)<>(r.raw_record->>'y')::float8 OR NOT ST_IsValid(b.location))) THEN RAISE EXCEPTION 'Point validation failed'; END IF;
 IF EXISTS(SELECT 1 FROM route_shapes WHERE shape_id=sid AND (ST_SRID(geometry)<>4326 OR ST_NPoints(geometry)<>868 OR NOT ST_IsValid(geometry))) THEN RAISE EXCEPTION 'Line validation failed'; END IF;
 IF EXISTS(SELECT 1 FROM route_shapes l CROSS JOIN LATERAL ST_DumpPoints(l.geometry) d JOIN route_shape_points p ON p.shape_id=l.shape_id AND p.line_seq=d.path[1] WHERE l.shape_id=sid AND (ST_X(d.geom)<>p.x OR ST_Y(d.geom)<>p.y)) THEN RAISE EXCEPTION 'Ordered coordinate validation failed'; END IF;
END $$;
CREATE OR REPLACE VIEW public.qgis_route_stops_current AS
 SELECT rs.*,b.stop_name,b.location,e.external_route_id AS gbis_route_id,b.crs_status AS stop_crs_status,b.crs_evidence AS stop_crs_evidence
 FROM public.route_stops rs JOIN public.bus_stops b USING(stop_id)
 JOIN public.route_external_ids e ON e.route_id=rs.route_id AND e.source_id='gbis'
 WHERE rs.pattern_id=(SELECT i.pattern_id FROM public.route_imports i WHERE i.route_id=rs.route_id ORDER BY i.observed_at DESC,i.input_hash DESC LIMIT 1);
COMMIT;
