-- Development-only technical logins. Application staff accounts are seeded separately.
\set ON_ERROR_STOP on
\getenv control_password CONTROL_DB_PASSWORD
\getenv auth_password AUTH_DB_PASSWORD
\getenv shard_password SHARD_DB_PASSWORD
BEGIN;
SELECT pg_advisory_xact_lock(170017,4);
CREATE TEMP TABLE runtime_accounts(name text,group_name text,password text) ON COMMIT DROP;
INSERT INTO runtime_accounts VALUES
('ci_app_control','ci_routing_reader',:'control_password'),
('ci_app_auth','ci_auth_runtime',:'auth_password'),
('ci_app_shard','ci_business_runtime',:'shard_password');
DO $$
DECLARE account record; existing pg_roles%ROWTYPE; marker text;
BEGIN
  FOR account IN SELECT * FROM runtime_accounts LOOP
    marker:='card-issuer-api compose runtime: '||account.group_name;
    SELECT * INTO existing FROM pg_roles WHERE rolname=account.name;
    IF FOUND THEN
      IF NOT existing.rolcanlogin OR NOT existing.rolinherit OR existing.rolsuper OR existing.rolcreatedb OR existing.rolcreaterole OR existing.rolreplication OR existing.rolbypassrls
         OR shobj_description(existing.oid,'pg_authid') IS DISTINCT FROM marker THEN
        RAISE EXCEPTION 'Existing runtime account % is incompatible; refusing takeover',account.name;
      END IF;
      IF (SELECT count(*) FROM pg_auth_members WHERE member=existing.oid)<>1
         OR NOT EXISTS (SELECT FROM pg_auth_members WHERE member=existing.oid AND roleid=account.group_name::regrole AND NOT admin_option AND inherit_option) THEN
        RAISE EXCEPTION 'Existing runtime account % has incompatible memberships',account.name;
      END IF;
      IF EXISTS (SELECT FROM pg_database WHERE datdba=existing.oid) THEN
        RAISE EXCEPTION 'Runtime account % must not own databases',account.name;
      END IF;
    ELSE
      EXECUTE format('CREATE ROLE %I LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L',account.name,account.password);
      EXECUTE format('GRANT %I TO %I WITH ADMIN FALSE',account.group_name,account.name);
      EXECUTE format('COMMENT ON ROLE %I IS %L',account.name,marker);
    END IF;
  END LOOP;
END $$;
COMMIT;
