CREATE TABLE control.command_idempotency_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operation_scope text NOT NULL CHECK (btrim(operation_scope)<>''),
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 256),
  request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint)=32),
  response_status smallint NOT NULL CHECK (response_status BETWEEN 200 AND 299),
  response_body jsonb NOT NULL CHECK (jsonb_typeof(response_body)='object'),
  result_reference uuid,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (operation_scope,idempotency_key),
  CHECK (expires_at>created_at)
);

CREATE FUNCTION control.guard_command_idempotency() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF TG_OP='UPDATE' THEN
    IF NEW.id<>OLD.id OR NEW.operation_scope<>OLD.operation_scope OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.created_at<>OLD.created_at THEN
      RAISE EXCEPTION 'Command idempotency identity is immutable';
    END IF;
    NEW.updated_at:=clock_timestamp();
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_command_idempotency BEFORE UPDATE ON control.command_idempotency_records
  FOR EACH ROW EXECUTE FUNCTION control.guard_command_idempotency();

CREATE INDEX command_idempotency_expiry ON control.command_idempotency_records(expires_at,id);
GRANT SELECT,INSERT,UPDATE ON control.command_idempotency_records TO ci_auth_runtime;
GRANT INSERT,UPDATE ON control.bank_routing_entries TO ci_auth_runtime;
