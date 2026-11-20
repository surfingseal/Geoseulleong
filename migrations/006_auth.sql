-- Explicit opt-in migration; never run automatically by the server.
BEGIN;
CREATE TABLE users (
 user_id UUID PRIMARY KEY,
 email TEXT NOT NULL CONSTRAINT users_email_unique UNIQUE,
 password_hash TEXT NOT NULL,
 nickname TEXT NOT NULL,
 nickname_key TEXT NOT NULL CONSTRAINT users_nickname_unique UNIQUE,
 status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK (email=lower(btrim(email)) AND char_length(email) BETWEEN 3 AND 254),
 CHECK (char_length(nickname) BETWEEN 2 AND 20)
);
CREATE TABLE user_consents (
 user_id UUID NOT NULL REFERENCES users(user_id),
 consent_type TEXT NOT NULL CHECK (consent_type IN ('terms','privacy')),
 version TEXT NOT NULL CHECK (char_length(version) BETWEEN 1 AND 64),
 agreed_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(user_id,consent_type,version)
);
CREATE TABLE refresh_tokens (
 token_id UUID PRIMARY KEY,
 family_id UUID NOT NULL,
 user_id UUID NOT NULL REFERENCES users(user_id),
 token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
 created_at TIMESTAMPTZ NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 revoked_at TIMESTAMPTZ,
 revoke_reason TEXT CHECK (revoke_reason IN ('rotated','logout','reuse')),
 replaced_by UUID,
 UNIQUE(token_id,user_id),
 FOREIGN KEY(family_id,user_id) REFERENCES refresh_tokens(token_id,user_id),
 FOREIGN KEY(replaced_by,user_id) REFERENCES refresh_tokens(token_id,user_id),
 CHECK(expires_at>created_at),
 CHECK((revoked_at IS NULL)=(revoke_reason IS NULL))
);
CREATE INDEX refresh_tokens_family ON refresh_tokens(family_id);
CREATE INDEX refresh_tokens_user_active ON refresh_tokens(user_id) WHERE revoked_at IS NULL;
CREATE INDEX refresh_tokens_expiry ON refresh_tokens(expires_at);
COMMIT;
