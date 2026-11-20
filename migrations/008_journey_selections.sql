-- Additive, no existing data is modified. Search selections retain the 10-minute API TTL.
BEGIN;
CREATE TABLE journey_selections (
 search_id TEXT PRIMARY KEY CHECK(search_id ~ '^[0-9a-f]{32}$'),
 payload JSONB NOT NULL CHECK(jsonb_typeof(payload)='object' AND octet_length(payload::text)<=2097152),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 expires_at TIMESTAMPTZ NOT NULL,
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '11 minutes')
);
CREATE INDEX journey_selections_expires_idx ON journey_selections(expires_at);
COMMIT;
