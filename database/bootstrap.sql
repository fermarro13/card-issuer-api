-- Administrative connection to postgres; CREATE DATABASE must be outside a transaction.
\set ON_ERROR_STOP on
SELECT pg_advisory_lock(170017, 1);
DO $$
DECLARE r text;
BEGIN
  IF current_setting('server_version_num')::int < 170000 THEN
    RAISE EXCEPTION 'PostgreSQL 17 or newer is required';
  END IF;
  FOREACH r IN ARRAY ARRAY['ci_owner','ci_auth_runtime','ci_routing_reader','ci_business_runtime','ci_executor_runtime'] LOOP
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', r);
    END IF;
    IF EXISTS (SELECT FROM pg_roles WHERE rolname=r AND
      (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)) THEN
      RAISE EXCEPTION 'Existing role % has incompatible attributes', r;
    END IF;
  END LOOP;
  FOREACH r IN ARRAY ARRAY['ci_auth_runtime','ci_routing_reader','ci_business_runtime','ci_executor_runtime'] LOOP
    IF EXISTS (SELECT FROM pg_auth_members m JOIN pg_roles p ON p.oid=m.member WHERE p.rolname=r) THEN
      RAISE EXCEPTION 'Runtime group % must not inherit other roles', r;
    END IF;
  END LOOP;
END $$;
SELECT format('CREATE DATABASE %I OWNER ci_owner', :'control_db')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname=:'control_db') \gexec
SELECT format('CREATE DATABASE %I OWNER ci_owner', :'shard_db')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname=:'shard_db') \gexec
SELECT (count(*) = 2 AND bool_and(datdba='ci_owner'::regrole)) AS valid_owners
FROM pg_database WHERE datname IN (:'control_db', :'shard_db') \gset
\if :valid_owners
\else
  DO $$ BEGIN RAISE EXCEPTION 'Database names must be distinct and owned by ci_owner; refusing takeover'; END $$;
\endif
REVOKE ALL ON DATABASE :"control_db" FROM PUBLIC;
REVOKE ALL ON DATABASE :"shard_db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"control_db" TO ci_auth_runtime, ci_routing_reader;
GRANT CONNECT ON DATABASE :"control_db" TO ci_executor_runtime;
GRANT CONNECT ON DATABASE :"shard_db" TO ci_business_runtime, ci_executor_runtime;
SELECT pg_advisory_unlock(170017, 1);
