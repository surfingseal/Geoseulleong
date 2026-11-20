-- Additive migration only. Apply after a database backup.
BEGIN;
CREATE TABLE IF NOT EXISTS external_api_budgets (
 scope TEXT PRIMARY KEY,
 budget_day DATE NOT NULL,
 daily_reserved INTEGER NOT NULL CHECK (daily_reserved >= 0),
 total_reserved BIGINT NOT NULL CHECK (total_reserved >= daily_reserved),
 last_reserved_at TIMESTAMPTZ
);
-- No zero-counter initialization: import the existing account ledger explicitly.
-- Use GREATEST on an existing scope; never reset or refund previous reservations.
COMMIT;
