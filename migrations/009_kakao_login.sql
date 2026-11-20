-- Back up the database before applying. Existing users and password hashes are preserved.
BEGIN;
SET LOCAL lock_timeout='5s';
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE users ADD CONSTRAINT users_local_password_requires_email CHECK(password_hash IS NULL OR email IS NOT NULL);
CREATE TABLE oauth_identities (
 provider TEXT NOT NULL CHECK(provider='kakao'),
 provider_user_id TEXT NOT NULL CHECK(provider_user_id ~ '^[1-9][0-9]{0,18}$'),
 user_id UUID NOT NULL REFERENCES users(user_id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(provider,provider_user_id),
 UNIQUE(user_id,provider)
);
COMMIT;
