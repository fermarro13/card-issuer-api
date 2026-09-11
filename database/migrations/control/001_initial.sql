CREATE SCHEMA control AUTHORIZATION ci_owner;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON SCHEMA control FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE ci_owner IN SCHEMA control REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

CREATE TABLE control.bank_routing_entries (
  entity_id uuid PRIMARY KEY,
  shard_id text NOT NULL CHECK (btrim(shard_id) <> ''),
  routing_version bigint NOT NULL DEFAULT 1 CHECK (routing_version > 0),
  placement_status text NOT NULL CHECK (placement_status IN ('provisioning','active','paused')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE control.users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  normalized_username text NOT NULL UNIQUE CHECK (normalized_username COLLATE "C" ~ '^[a-z0-9][a-z0-9_.@-]{0,127}$'),
  password_hash text NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
  role text NOT NULL CHECK (role IN ('issuer_operator','issuer_readonly','bank_operator','bank_readonly')),
  entity_id uuid REFERENCES control.bank_routing_entries(entity_id),
  status text NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled','disabled')),
  auth_version bigint NOT NULL DEFAULT 1 CHECK (auth_version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES control.users(id),
  updated_by uuid REFERENCES control.users(id),
  CHECK ((role IN ('bank_operator','bank_readonly')) = (entity_id IS NOT NULL))
);
CREATE TABLE control.auth_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES control.users(id),
  auth_version bigint NOT NULL CHECK (auth_version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT (now() + interval '7 days'),
  last_refreshed_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  revocation_reason text,
  CHECK (expires_at > created_at AND expires_at <= created_at + interval '7 days'),
  CHECK (last_refreshed_at >= created_at AND last_refreshed_at <= expires_at),
  CHECK ((revoked_at IS NULL) = (revocation_reason IS NULL))
);
CREATE TABLE control.refresh_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  session_id uuid NOT NULL REFERENCES control.auth_sessions(id),
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
  parent_token_id uuid,
  issued_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  UNIQUE (session_id,id),
  UNIQUE (parent_token_id),
  FOREIGN KEY (session_id,parent_token_id) REFERENCES control.refresh_tokens(session_id,id),
  CHECK (parent_token_id IS DISTINCT FROM id),
  CHECK (expires_at > issued_at),
  CHECK (consumed_at IS NULL OR consumed_at >= issued_at)
);
CREATE UNIQUE INDEX refresh_tokens_one_root ON control.refresh_tokens(session_id) WHERE parent_token_id IS NULL;
CREATE TABLE control.authentication_audit_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_type text NOT NULL CHECK (btrim(event_type)<>''),
  outcome text NOT NULL CHECK (outcome IN ('succeeded','failed','rejected')),
  actor_user_id uuid REFERENCES control.users(id),
  subject_user_id uuid REFERENCES control.users(id),
  entity_id uuid REFERENCES control.bank_routing_entries(entity_id),
  session_id uuid REFERENCES control.auth_sessions(id),
  request_id uuid NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  details jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details)='object')
);

CREATE FUNCTION control.guard_user() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  NEW.normalized_username := lower(btrim(NEW.normalized_username) COLLATE "C");
  IF TG_OP='UPDATE' THEN
    IF NEW.id<>OLD.id OR NEW.created_at<>OLD.created_at OR NEW.created_by IS DISTINCT FROM OLD.created_by THEN
      RAISE EXCEPTION 'User identity and creation attribution are immutable';
    END IF;
    IF NEW.auth_version < OLD.auth_version THEN RAISE EXCEPTION 'auth_version cannot decrease'; END IF;
    IF ROW(NEW.password_hash,NEW.role,NEW.entity_id,NEW.status) IS DISTINCT FROM
       ROW(OLD.password_hash,OLD.role,OLD.entity_id,OLD.status) THEN
      NEW.auth_version := greatest(NEW.auth_version,OLD.auth_version+1);
    END IF;
    NEW.updated_at := clock_timestamp();
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_user BEFORE INSERT OR UPDATE ON control.users FOR EACH ROW EXECUTE FUNCTION control.guard_user();

CREATE FUNCTION control.guard_session() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF ROW(NEW.id,NEW.user_id,NEW.auth_version,NEW.created_at,NEW.expires_at) IS DISTINCT FROM
     ROW(OLD.id,OLD.user_id,OLD.auth_version,OLD.created_at,OLD.expires_at) THEN
    RAISE EXCEPTION 'Session identity, captured authorization and absolute expiry are immutable';
  END IF;
  IF OLD.revoked_at IS NOT NULL AND ROW(NEW.revoked_at,NEW.revocation_reason) IS DISTINCT FROM ROW(OLD.revoked_at,OLD.revocation_reason) THEN
    RAISE EXCEPTION 'Session revocation is permanent';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_session BEFORE UPDATE ON control.auth_sessions FOR EACH ROW EXECUTE FUNCTION control.guard_session();

CREATE FUNCTION control.guard_refresh_token() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE family control.auth_sessions%ROWTYPE;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF ROW(NEW.id,NEW.session_id,NEW.token_hash,NEW.parent_token_id,NEW.issued_at,NEW.expires_at) IS DISTINCT FROM
       ROW(OLD.id,OLD.session_id,OLD.token_hash,OLD.parent_token_id,OLD.issued_at,OLD.expires_at) THEN
      RAISE EXCEPTION 'Refresh-token identity, ancestry and lifetime are immutable';
    END IF;
    IF OLD.consumed_at IS NOT NULL AND NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
      RAISE EXCEPTION 'Consumed token evidence is immutable';
    END IF;
  ELSE
    SELECT * INTO STRICT family FROM control.auth_sessions WHERE id=NEW.session_id FOR UPDATE;
    IF NEW.expires_at > family.expires_at OR NEW.issued_at < family.created_at THEN
      RAISE EXCEPTION 'Refresh token must fit within session lifetime';
    END IF;
    IF family.revoked_at IS NOT NULL THEN RAISE EXCEPTION 'Session is revoked'; END IF;
    IF NEW.parent_token_id IS NOT NULL AND NOT EXISTS (
      SELECT FROM control.refresh_tokens
      WHERE id=NEW.parent_token_id AND session_id=NEW.session_id AND issued_at<=NEW.issued_at
    ) THEN
      RAISE EXCEPTION 'Token parent must already exist in this session and precede its successor' USING ERRCODE='23503';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_refresh_token BEFORE INSERT OR UPDATE ON control.refresh_tokens FOR EACH ROW EXECUTE FUNCTION control.guard_refresh_token();

CREATE INDEX users_bank_role ON control.users(entity_id,role,status);
CREATE INDEX sessions_user ON control.auth_sessions(user_id,created_at);
CREATE INDEX sessions_expiry ON control.auth_sessions(expires_at,id);
CREATE INDEX tokens_session ON control.refresh_tokens(session_id,issued_at);
CREATE INDEX tokens_expiry ON control.refresh_tokens(expires_at,id);
CREATE INDEX auth_audit_time ON control.authentication_audit_events(occurred_at DESC,id DESC);
CREATE INDEX auth_audit_subject ON control.authentication_audit_events(subject_user_id,occurred_at DESC,id DESC);
GRANT USAGE ON SCHEMA control TO ci_auth_runtime,ci_routing_reader;
GRANT SELECT ON control.bank_routing_entries TO ci_auth_runtime,ci_routing_reader;
GRANT SELECT,INSERT,UPDATE ON control.users,control.auth_sessions,control.refresh_tokens TO ci_auth_runtime;
GRANT SELECT,INSERT ON control.authentication_audit_events TO ci_auth_runtime;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA control FROM PUBLIC;
