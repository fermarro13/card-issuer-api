CREATE SCHEMA bank AUTHORIZATION ci_owner;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON SCHEMA bank FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE ci_owner IN SCHEMA bank REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

CREATE DOMAIN bank.card_state AS text CHECK (VALUE IN ('pending','issued','active','suspended','closed','expired'));
CREATE DOMAIN bank.execution_state AS text CHECK (VALUE IN ('queued','processing','succeeded','failed'));

CREATE TABLE bank.entities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  bank_reference text NOT NULL UNIQUE CHECK (btrim(bank_reference)<>''),
  name text NOT NULL CHECK (btrim(name)<>''),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL
);

CREATE TABLE bank.clients (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  external_client_ref text NOT NULL CHECK (btrim(external_client_ref)<>''),
  display_name text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,external_client_ref)
);

CREATE TABLE bank.account_references (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  client_id uuid NOT NULL,
  external_account_ref text NOT NULL CHECK (btrim(external_account_ref)<>''),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,external_account_ref),
  UNIQUE (entity_id,client_id,id),
  FOREIGN KEY (entity_id,client_id) REFERENCES bank.clients(entity_id,id)
);

CREATE TABLE bank.card_products (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  product_code text NOT NULL CHECK (btrim(product_code)<>''),
  name text NOT NULL CHECK (btrim(name)<>''),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
  configuration jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(configuration)='object'),
  configuration_version bigint NOT NULL DEFAULT 1 CHECK (configuration_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,product_code)
);

CREATE TABLE bank.cards (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  client_id uuid NOT NULL,
  account_reference_id uuid NOT NULL,
  product_id uuid NOT NULL,
  status bank.card_state NOT NULL DEFAULT 'pending',
  credential_reference text CHECK (credential_reference IS NULL OR btrim(credential_reference)<>''),
  predecessor_card_id uuid,
  issued_at timestamptz,
  activated_at timestamptz,
  suspended_at timestamptz,
  closed_at timestamptz,
  expires_at timestamptz,
  version bigint NOT NULL DEFAULT 1 CHECK (version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  CHECK (predecessor_card_id IS DISTINCT FROM id),
  FOREIGN KEY (entity_id,client_id) REFERENCES bank.clients(entity_id,id),
  FOREIGN KEY (entity_id,client_id,account_reference_id) REFERENCES bank.account_references(entity_id,client_id,id),
  FOREIGN KEY (entity_id,product_id) REFERENCES bank.card_products(entity_id,id),
  FOREIGN KEY (entity_id,predecessor_card_id) REFERENCES bank.cards(entity_id,id)
);

CREATE TABLE bank.card_operations (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  card_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('issue','activate','suspend','resume','close','expire','replace')),
  status bank.execution_state NOT NULL DEFAULT 'queued',
  reason text NOT NULL CHECK (btrim(reason)<>''),
  actor_user_id uuid NOT NULL,
  actor_role text NOT NULL CHECK (actor_role IN ('issuer_operator','bank_operator')),
  actor_entity_id uuid,
  executor_identity text,
  request_id uuid NOT NULL,
  started_at timestamptz,
  completed_at timestamptz,
  failure_code text,
  failure_summary text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,card_id,id),
  FOREIGN KEY (entity_id,card_id) REFERENCES bank.cards(entity_id,id),
  CHECK ((actor_role='bank_operator' AND actor_entity_id IS NOT NULL AND actor_entity_id=entity_id) OR
         (actor_role='issuer_operator' AND actor_entity_id IS NULL))
);

CREATE TABLE bank.card_status_history (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  card_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  previous_status bank.card_state NOT NULL,
  new_status bank.card_state NOT NULL,
  reason text NOT NULL CHECK (btrim(reason)<>''),
  actor_user_id uuid NOT NULL,
  actor_role text NOT NULL CHECK (actor_role IN ('issuer_operator','bank_operator')),
  actor_entity_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,operation_id),
  CHECK (previous_status<>new_status),
  FOREIGN KEY (entity_id,card_id) REFERENCES bank.cards(entity_id,id),
  FOREIGN KEY (entity_id,card_id,operation_id) REFERENCES bank.card_operations(entity_id,card_id,id),
  CHECK ((actor_role='bank_operator' AND actor_entity_id IS NOT NULL AND actor_entity_id=entity_id) OR
         (actor_role='issuer_operator' AND actor_entity_id IS NULL))
);

CREATE TABLE bank.audit_events (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  actor_user_id uuid NOT NULL,
  actor_role text NOT NULL CHECK (actor_role IN ('issuer_operator','issuer_readonly','bank_operator','bank_readonly')),
  actor_entity_id uuid,
  executor_identity text,
  action text NOT NULL CHECK (btrim(action)<>''),
  resource_type text NOT NULL CHECK (btrim(resource_type)<>''),
  resource_id uuid,
  outcome text NOT NULL CHECK (outcome IN ('succeeded','failed','rejected')),
  request_id uuid NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  details jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details)='object'),
  PRIMARY KEY (entity_id,id),
  CHECK ((actor_role IN ('bank_operator','bank_readonly') AND actor_entity_id IS NOT NULL AND actor_entity_id=entity_id) OR
         (actor_role IN ('issuer_operator','issuer_readonly') AND actor_entity_id IS NULL))
);

CREATE TABLE bank.idempotency_records (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  operation_scope text NOT NULL CHECK (btrim(operation_scope)<>''),
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 256),
  request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint)=32),
  status text NOT NULL DEFAULT 'processing' CHECK (status IN ('processing','succeeded','failed')),
  result_reference uuid,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,operation_scope,idempotency_key),
  CHECK (expires_at>created_at)
);

CREATE TABLE bank.card_status_batches (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  target_status bank.card_state NOT NULL,
  reason text NOT NULL CHECK (btrim(reason)<>''),
  requested_by uuid NOT NULL,
  requester_role text NOT NULL CHECK (requester_role IN ('issuer_operator','bank_operator')),
  requester_entity_id uuid,
  request_id uuid NOT NULL,
  idempotency_record_id uuid NOT NULL,
  status bank.execution_state NOT NULL DEFAULT 'queued',
  item_count integer NOT NULL CHECK (item_count>0),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text,
  lease_expires_at timestamptz,
  lease_version bigint NOT NULL DEFAULT 0 CHECK (lease_version>=0),
  started_at timestamptz,
  completed_at timestamptz,
  failure_code text,
  failure_summary text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,idempotency_record_id),
  FOREIGN KEY (entity_id,idempotency_record_id) REFERENCES bank.idempotency_records(entity_id,id),
  CHECK ((lease_owner IS NULL)=(lease_expires_at IS NULL)),
  CHECK ((requester_role='bank_operator' AND requester_entity_id IS NOT NULL AND requester_entity_id=entity_id) OR
         (requester_role='issuer_operator' AND requester_entity_id IS NULL))
);

CREATE TABLE bank.card_status_batch_items (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  batch_id uuid NOT NULL,
  card_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  previous_status bank.card_state,
  outcome text NOT NULL DEFAULT 'pending' CHECK (outcome IN ('pending','applied','not_applied')),
  failure_code text,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,batch_id,card_id),
  UNIQUE (entity_id,operation_id),
  FOREIGN KEY (entity_id,batch_id) REFERENCES bank.card_status_batches(entity_id,id),
  FOREIGN KEY (entity_id,card_id) REFERENCES bank.cards(entity_id,id),
  FOREIGN KEY (entity_id,card_id,operation_id) REFERENCES bank.card_operations(entity_id,card_id,id),
  CHECK ((outcome='applied')=(previous_status IS NOT NULL))
);

CREATE TABLE bank.outbox_messages (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  work_type text NOT NULL CHECK (btrim(work_type)<>''),
  operation_id uuid,
  batch_id uuid,
  payload jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(payload)='object'),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered','failed')),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text,
  lease_expires_at timestamptz,
  lease_version bigint NOT NULL DEFAULT 0 CHECK (lease_version>=0),
  delivered_at timestamptz,
  failure_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  PRIMARY KEY (entity_id,id),
  FOREIGN KEY (entity_id,operation_id) REFERENCES bank.card_operations(entity_id,id),
  FOREIGN KEY (entity_id,batch_id) REFERENCES bank.card_status_batches(entity_id,id),
  CHECK (num_nonnulls(operation_id,batch_id)=1),
  CHECK ((lease_owner IS NULL)=(lease_expires_at IS NULL))
);

CREATE FUNCTION bank.current_entity_id() RETURNS uuid LANGUAGE sql STABLE SET search_path=pg_catalog AS $$
  SELECT nullif(current_setting('app.entity_id',true),'')::uuid
$$;
CREATE FUNCTION bank.guard_identity() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF NEW.id<>OLD.id OR (to_jsonb(NEW)->'entity_id') IS DISTINCT FROM (to_jsonb(OLD)->'entity_id') THEN
    RAISE EXCEPTION 'Identity and tenant ownership are immutable';
  END IF;
  IF to_jsonb(OLD) ? 'created_at' THEN
    IF ROW(to_jsonb(NEW)->'created_at',to_jsonb(NEW)->'created_by') IS DISTINCT FROM
       ROW(to_jsonb(OLD)->'created_at',to_jsonb(OLD)->'created_by') THEN
      RAISE EXCEPTION 'Creation attribution is immutable';
    END IF;
  END IF;
  IF to_jsonb(OLD) ? 'updated_at' THEN NEW.updated_at := clock_timestamp(); END IF;
  RETURN NEW;
END $$;
CREATE FUNCTION bank.guard_batch_request() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF ROW(NEW.target_status,NEW.reason,NEW.requested_by,NEW.requester_role,NEW.requester_entity_id,NEW.request_id,NEW.idempotency_record_id,NEW.item_count)
     IS DISTINCT FROM
     ROW(OLD.target_status,OLD.reason,OLD.requested_by,OLD.requester_role,OLD.requester_entity_id,OLD.request_id,OLD.idempotency_record_id,OLD.item_count) THEN
    RAISE EXCEPTION 'Accepted batch request is immutable';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_batch_request BEFORE UPDATE ON bank.card_status_batches FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_request();
CREATE FUNCTION bank.guard_batch_item() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF ROW(NEW.batch_id,NEW.card_id,NEW.operation_id) IS DISTINCT FROM ROW(OLD.batch_id,OLD.card_id,OLD.operation_id) THEN
    RAISE EXCEPTION 'Batch item membership is immutable';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_batch_item BEFORE UPDATE ON bank.card_status_batch_items FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_item();

CREATE FUNCTION bank.guard_operation() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF ROW(NEW.card_id,NEW.action,NEW.reason,NEW.actor_user_id,NEW.actor_role,NEW.actor_entity_id,NEW.request_id)
     IS DISTINCT FROM
     ROW(OLD.card_id,OLD.action,OLD.reason,OLD.actor_user_id,OLD.actor_role,OLD.actor_entity_id,OLD.request_id) THEN
    RAISE EXCEPTION 'Accepted operation and initiating attribution are immutable';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_operation BEFORE UPDATE ON bank.card_operations FOR EACH ROW EXECUTE FUNCTION bank.guard_operation();

ALTER TABLE bank.entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.entities FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.entities USING (id=bank.current_entity_id()) WITH CHECK (id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.entities FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.clients ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.clients FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.clients USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.clients FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.account_references ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.account_references FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.account_references USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.account_references FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.card_products ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_products FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_products USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_products FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.cards ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.cards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.cards USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.cards FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.card_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_operations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_operations USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_operations FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.card_status_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_status_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_status_history USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_status_history FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.audit_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.audit_events USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.audit_events FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.idempotency_records FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.idempotency_records USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.idempotency_records FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.card_status_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_status_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_status_batches USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_status_batches FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.card_status_batch_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_status_batch_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_status_batch_items USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_status_batch_items FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.outbox_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.outbox_messages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.outbox_messages USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.outbox_messages FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

CREATE INDEX clients_list ON bank.clients(entity_id,created_at DESC,id DESC);
CREATE INDEX cards_list ON bank.cards(entity_id,created_at DESC,id DESC);
CREATE INDEX cards_client ON bank.cards(entity_id,client_id,created_at DESC,id DESC);
CREATE INDEX cards_status ON bank.cards(entity_id,status,created_at DESC,id DESC);
CREATE INDEX cards_account ON bank.cards(entity_id,account_reference_id);
CREATE INDEX cards_product ON bank.cards(entity_id,product_id);
CREATE INDEX cards_predecessor ON bank.cards(entity_id,predecessor_card_id);
CREATE INDEX operations_card ON bank.card_operations(entity_id,card_id,created_at DESC,id DESC);
CREATE INDEX history_card ON bank.card_status_history(entity_id,card_id,created_at DESC,id DESC);
CREATE INDEX audit_time ON bank.audit_events(entity_id,occurred_at DESC,id DESC);
CREATE INDEX audit_resource ON bank.audit_events(entity_id,resource_type,resource_id,occurred_at DESC,id DESC);
CREATE INDEX batches_list ON bank.card_status_batches(entity_id,created_at DESC,id DESC);
CREATE INDEX batches_status ON bank.card_status_batches(entity_id,status,created_at DESC,id DESC);
CREATE INDEX batches_queue ON bank.card_status_batches(entity_id,next_attempt_at,id) WHERE status='queued';
CREATE INDEX batches_recovery ON bank.card_status_batches(entity_id,lease_expires_at,id) WHERE status='processing';
CREATE INDEX batch_items_card ON bank.card_status_batch_items(entity_id,card_id,batch_id);
CREATE INDEX outbox_work ON bank.outbox_messages(entity_id,next_attempt_at,id) WHERE status IN ('pending','processing');
CREATE INDEX outbox_operation ON bank.outbox_messages(entity_id,operation_id);
CREATE INDEX outbox_batch ON bank.outbox_messages(entity_id,batch_id);
GRANT USAGE ON SCHEMA bank TO ci_business_runtime;
GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA bank TO ci_business_runtime;
REVOKE UPDATE ON bank.audit_events,bank.card_status_history FROM ci_business_runtime;

ALTER DOMAIN bank.execution_state DROP CONSTRAINT execution_state_check;
ALTER DOMAIN bank.execution_state ADD CONSTRAINT execution_state_check
  CHECK (VALUE IN ('draft','queued','processing','succeeded','failed','cancelled'));

ALTER TABLE bank.card_operations ADD CONSTRAINT card_operations_execution_state_check
  CHECK (status IN ('queued','processing','succeeded','failed'));

ALTER TABLE bank.card_status_batch_items DROP CONSTRAINT card_status_batch_items_outcome_check;
ALTER TABLE bank.card_status_batch_items ADD CONSTRAINT card_status_batch_items_outcome_check
  CHECK (outcome IN ('pending','applied','not_applied','ignored'));

ALTER TABLE bank.card_status_batches
  ALTER COLUMN status SET DEFAULT 'draft',
  ADD COLUMN applied_count integer NOT NULL DEFAULT 0 CHECK (applied_count>=0),
  ADD COLUMN ignored_count integer NOT NULL DEFAULT 0 CHECK (ignored_count>=0),
  ADD COLUMN retry_of_batch_id uuid,
  ADD CONSTRAINT card_status_batches_result_counts_check
    CHECK (applied_count+ignored_count<=item_count),
  ADD CONSTRAINT card_status_batches_retry_of_batch_fkey
    FOREIGN KEY (entity_id,retry_of_batch_id) REFERENCES bank.card_status_batches(entity_id,id),
  ADD CONSTRAINT card_status_batches_lease_status_check
    CHECK ((status='processing')=(lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL));

CREATE OR REPLACE FUNCTION bank.guard_batch_request() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF ROW(NEW.target_status,NEW.reason,NEW.requested_by,NEW.requester_role,NEW.requester_entity_id,NEW.request_id,NEW.idempotency_record_id,NEW.item_count,NEW.retry_of_batch_id)
     IS DISTINCT FROM
     ROW(OLD.target_status,OLD.reason,OLD.requested_by,OLD.requester_role,OLD.requester_entity_id,OLD.request_id,OLD.idempotency_record_id,OLD.item_count,OLD.retry_of_batch_id) THEN
    RAISE EXCEPTION 'Accepted batch request is immutable';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION bank.guard_batch_retry_provenance() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE source_batch bank.card_status_batches%ROWTYPE;
BEGIN
  IF NEW.retry_of_batch_id IS NULL THEN
    RETURN NEW;
  END IF;
  IF TG_OP='UPDATE' AND NEW.retry_of_batch_id IS NOT DISTINCT FROM OLD.retry_of_batch_id THEN
    RETURN NEW;
  END IF;
  IF NEW.retry_of_batch_id=NEW.id THEN
    RAISE EXCEPTION 'A batch cannot retry itself';
  END IF;
  SELECT * INTO source_batch
    FROM bank.card_status_batches
    WHERE entity_id=NEW.entity_id AND id=NEW.retry_of_batch_id;
  IF NOT FOUND OR source_batch.status NOT IN ('failed','cancelled') THEN
    RAISE EXCEPTION 'Batch retry source must be terminal failed or cancelled work';
  END IF;
  IF NEW.status<>'draft' OR ROW(NEW.target_status,NEW.reason) IS DISTINCT FROM ROW(source_batch.target_status,source_batch.reason) THEN
    RAISE EXCEPTION 'Batch retry must be a draft with the source target and reason';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER guard_batch_retry_provenance BEFORE INSERT OR UPDATE ON bank.card_status_batches
  FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_retry_provenance();

CREATE FUNCTION bank.guard_batch_state() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE
  applied_items integer;
  ignored_items integer;
  unresolved_items integer;
BEGIN
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    IF NEW.status='processing' AND
       (NEW.lease_owner IS DISTINCT FROM OLD.lease_owner OR NEW.lease_version<>OLD.lease_version) THEN
      RAISE EXCEPTION 'A processing batch lease owner and fencing version are immutable';
    END IF;
    IF NEW.status='processing' AND (btrim(NEW.lease_owner)='' OR NEW.lease_expires_at<=clock_timestamp()) THEN
      RAISE EXCEPTION 'A processing batch must retain a nonempty current lease';
    END IF;
    IF NEW.status IN ('succeeded','failed','cancelled') AND
       ROW(NEW.applied_count,NEW.ignored_count,NEW.completed_at,NEW.failure_code,NEW.failure_summary)
       IS DISTINCT FROM ROW(OLD.applied_count,OLD.ignored_count,OLD.completed_at,OLD.failure_code,OLD.failure_summary) THEN
      RAISE EXCEPTION 'Terminal batch results are immutable';
    END IF;
    RETURN NEW;
  END IF;

  CASE OLD.status
    WHEN 'draft' THEN
      IF NEW.status NOT IN ('queued','cancelled') THEN RAISE EXCEPTION 'Invalid batch state transition'; END IF;
    WHEN 'queued' THEN
      IF NEW.status NOT IN ('processing','cancelled') THEN RAISE EXCEPTION 'Invalid batch state transition'; END IF;
    WHEN 'processing' THEN
      IF NEW.status NOT IN ('queued','succeeded','failed') THEN RAISE EXCEPTION 'Invalid batch state transition'; END IF;
    ELSE
      RAISE EXCEPTION 'Terminal batches cannot transition';
  END CASE;

  IF NEW.status='processing' THEN
    IF NEW.lease_version<>OLD.lease_version+1 OR btrim(NEW.lease_owner)='' OR NEW.lease_expires_at<=clock_timestamp() THEN
      RAISE EXCEPTION 'Claiming a batch requires a nonempty current lease and increments its fencing version';
    END IF;
  ELSIF NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL THEN
    RAISE EXCEPTION 'Only processing batches may hold a lease';
  END IF;

  IF OLD.status='draft' AND NEW.status='queued' THEN
    SELECT count(*) INTO unresolved_items
      FROM bank.card_status_batch_items
      WHERE entity_id=NEW.entity_id AND batch_id=NEW.id;
    IF unresolved_items<>NEW.item_count OR NEW.applied_count<>0 OR NEW.ignored_count<>0 THEN
      RAISE EXCEPTION 'A draft must have its complete unprocessed membership before dispatch';
    END IF;
  END IF;

  IF NEW.status='succeeded' THEN
    SELECT count(*) FILTER (WHERE outcome='applied'),
           count(*) FILTER (WHERE outcome='ignored'),
           count(*) FILTER (WHERE outcome NOT IN ('applied','ignored'))
      INTO applied_items,ignored_items,unresolved_items
      FROM bank.card_status_batch_items
      WHERE entity_id=NEW.entity_id AND batch_id=NEW.id;
    IF applied_items<>NEW.applied_count OR ignored_items<>NEW.ignored_count OR
       unresolved_items<>0 OR applied_items+ignored_items<>NEW.item_count THEN
      RAISE EXCEPTION 'Succeeded batch results must exactly match every item outcome';
    END IF;
    IF NEW.completed_at IS NULL THEN RAISE EXCEPTION 'Succeeded batches require a completion time'; END IF;
  ELSIF NEW.status='failed' THEN
    SELECT count(*) FILTER (WHERE outcome='ignored'),
           count(*) FILTER (WHERE outcome NOT IN ('ignored','not_applied'))
      INTO ignored_items,unresolved_items
      FROM bank.card_status_batch_items
      WHERE entity_id=NEW.entity_id AND batch_id=NEW.id;
    IF NEW.applied_count<>0 OR NEW.ignored_count<>ignored_items OR unresolved_items<>0 OR NEW.completed_at IS NULL THEN
      RAISE EXCEPTION 'Failed batch results must be unapplied or ignored with matching terminal counts';
    END IF;
  ELSIF NEW.status='cancelled' THEN
    IF NEW.applied_count<>0 OR NEW.ignored_count<>0 OR NEW.completed_at IS NULL THEN
      RAISE EXCEPTION 'Cancelled batches require no results and a completion time';
    END IF;
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER guard_batch_state BEFORE UPDATE ON bank.card_status_batches
  FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_state();

CREATE FUNCTION bank.guard_batch_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF NEW.status<>'draft' OR NEW.applied_count<>0 OR NEW.ignored_count<>0 OR
     NEW.attempt_count<>0 OR NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR
     NEW.lease_version<>0 OR NEW.started_at IS NOT NULL OR NEW.completed_at IS NOT NULL OR
     NEW.failure_code IS NOT NULL OR NEW.failure_summary IS NOT NULL THEN
    RAISE EXCEPTION 'New public batches must be unprocessed drafts';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_batch_insert BEFORE INSERT ON bank.card_status_batches
  FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_insert();

CREATE FUNCTION bank.guard_batch_item_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE batch_status bank.execution_state;
BEGIN
  SELECT status INTO batch_status
    FROM bank.card_status_batches
    WHERE entity_id=NEW.entity_id AND id=NEW.batch_id;
  IF batch_status IS DISTINCT FROM 'draft' THEN
    RAISE EXCEPTION 'Batch item membership may be added only to a draft';
  END IF;
  IF NEW.outcome<>'pending' OR NEW.previous_status IS NOT NULL OR NEW.failure_code IS NOT NULL THEN
    RAISE EXCEPTION 'New batch items must be unprocessed';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_batch_item_insert BEFORE INSERT ON bank.card_status_batch_items
  FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_item_insert();

CREATE FUNCTION bank.guard_batch_item_result() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE batch_status bank.execution_state;
BEGIN
  IF ROW(NEW.outcome,NEW.previous_status,NEW.failure_code) IS DISTINCT FROM ROW(OLD.outcome,OLD.previous_status,OLD.failure_code) THEN
    SELECT status INTO batch_status
      FROM bank.card_status_batches
      WHERE entity_id=NEW.entity_id AND id=NEW.batch_id;
    IF batch_status IS DISTINCT FROM 'processing' THEN
      RAISE EXCEPTION 'Batch item results may be recorded only by a processing batch';
    END IF;
    IF OLD.outcome<>'pending' OR NEW.outcome NOT IN ('applied','not_applied','ignored') THEN
      RAISE EXCEPTION 'Batch item results are terminal and may only resolve pending work';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_batch_item_result BEFORE UPDATE ON bank.card_status_batch_items
  FOR EACH ROW EXECUTE FUNCTION bank.guard_batch_item_result();

ALTER TABLE bank.card_operations ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE bank.card_operations ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE bank.card_operations ALTER COLUMN updated_by DROP NOT NULL;
ALTER TABLE bank.card_operations DROP CONSTRAINT card_operations_actor_role_check;
ALTER TABLE bank.card_operations ADD CONSTRAINT card_operations_actor_role_check
  CHECK (actor_role IN ('issuer_operator','bank_operator','system'));
ALTER TABLE bank.card_operations DROP CONSTRAINT card_operations_check;
ALTER TABLE bank.card_operations ADD CONSTRAINT card_operations_actor_context_check CHECK (
  (actor_role='bank_operator' AND actor_user_id IS NOT NULL AND actor_entity_id IS NOT NULL AND actor_entity_id=entity_id) OR
  (actor_role='issuer_operator' AND actor_user_id IS NOT NULL AND actor_entity_id IS NULL) OR
  (actor_role='system' AND actor_user_id IS NULL AND actor_entity_id IS NULL AND executor_identity IS NOT NULL AND btrim(executor_identity)<>'')
);

ALTER TABLE bank.card_status_history ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE bank.card_status_history ADD COLUMN executor_identity text;
ALTER TABLE bank.card_status_history DROP CONSTRAINT card_status_history_actor_role_check;
ALTER TABLE bank.card_status_history ADD CONSTRAINT card_status_history_actor_role_check
  CHECK (actor_role IN ('issuer_operator','bank_operator','system'));
ALTER TABLE bank.card_status_history DROP CONSTRAINT card_status_history_check;
ALTER TABLE bank.card_status_history ADD CONSTRAINT card_status_history_actor_context_check CHECK (
  (actor_role='bank_operator' AND actor_user_id IS NOT NULL AND actor_entity_id IS NOT NULL AND actor_entity_id=entity_id) OR
  (actor_role='issuer_operator' AND actor_user_id IS NOT NULL AND actor_entity_id IS NULL) OR
  (actor_role='system' AND actor_user_id IS NULL AND actor_entity_id IS NULL AND executor_identity IS NOT NULL AND btrim(executor_identity)<>'')
);

ALTER TABLE bank.audit_events ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE bank.audit_events DROP CONSTRAINT audit_events_actor_role_check;
ALTER TABLE bank.audit_events ADD CONSTRAINT audit_events_actor_role_check
  CHECK (actor_role IN ('issuer_operator','issuer_readonly','bank_operator','bank_readonly','system'));
ALTER TABLE bank.audit_events DROP CONSTRAINT audit_events_check;
ALTER TABLE bank.audit_events ADD CONSTRAINT audit_events_actor_context_check CHECK (
  (actor_role IN ('bank_operator','bank_readonly') AND actor_user_id IS NOT NULL AND actor_entity_id IS NOT NULL AND actor_entity_id=entity_id) OR
  (actor_role IN ('issuer_operator','issuer_readonly') AND actor_user_id IS NOT NULL AND actor_entity_id IS NULL) OR
  (actor_role='system' AND actor_user_id IS NULL AND actor_entity_id IS NULL AND executor_identity IS NOT NULL AND btrim(executor_identity)<>'')
);

CREATE TABLE bank.card_expiry_runs (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  run_date date NOT NULL,
  status text NOT NULL DEFAULT 'processing' CHECK (status IN ('processing','completed')),
  item_count integer NOT NULL DEFAULT 0 CHECK (item_count>=0),
  expired_count integer NOT NULL DEFAULT 0 CHECK (expired_count>=0),
  skipped_already_expired_count integer NOT NULL DEFAULT 0 CHECK (skipped_already_expired_count>=0),
  manual_retry_required_count integer NOT NULL DEFAULT 0 CHECK (manual_retry_required_count>=0),
  executor_identity text NOT NULL CHECK (btrim(executor_identity)<>''),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,run_date),
  CHECK (expired_count+skipped_already_expired_count+manual_retry_required_count<=item_count)
);

CREATE TABLE bank.card_expiry_run_items (
  entity_id uuid NOT NULL REFERENCES bank.entities(id),
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  expiry_run_id uuid NOT NULL,
  card_id uuid NOT NULL,
  operation_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','expired','skipped_already_expired','manual_retry_required')),
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0),
  automatic_retry_count integer NOT NULL DEFAULT 0 CHECK (automatic_retry_count BETWEEN 0 AND 3),
  manual_retry_count integer NOT NULL DEFAULT 0 CHECK (manual_retry_count>=0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text,
  lease_expires_at timestamptz,
  lease_version bigint NOT NULL DEFAULT 0 CHECK (lease_version>=0),
  failure_code text,
  manual_retry_by uuid,
  manual_retry_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_id,id),
  UNIQUE (entity_id,expiry_run_id,card_id),
  UNIQUE (entity_id,operation_id),
  FOREIGN KEY (entity_id,expiry_run_id) REFERENCES bank.card_expiry_runs(entity_id,id),
  FOREIGN KEY (entity_id,card_id) REFERENCES bank.cards(entity_id,id),
  FOREIGN KEY (entity_id,card_id,operation_id) REFERENCES bank.card_operations(entity_id,card_id,id),
  CHECK ((status='processing')=(lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)),
  CHECK ((manual_retry_by IS NULL)=(manual_retry_at IS NULL))
);

CREATE FUNCTION bank.guard_expiry_run() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE
  expired_items integer;
  skipped_items integer;
  manual_items integer;
  unresolved_items integer;
BEGIN
  IF NEW.run_date<>OLD.run_date OR NEW.executor_identity<>OLD.executor_identity THEN
    RAISE EXCEPTION 'Expiry run identity is immutable';
  END IF;
  IF NEW.status='completed' AND NEW.status IS NOT DISTINCT FROM OLD.status AND
     ROW(NEW.item_count,NEW.expired_count,NEW.skipped_already_expired_count,NEW.manual_retry_required_count,NEW.completed_at)
     IS DISTINCT FROM ROW(OLD.item_count,OLD.expired_count,OLD.skipped_already_expired_count,OLD.manual_retry_required_count,OLD.completed_at) THEN
    RAISE EXCEPTION 'Completed expiry run results are immutable';
  END IF;
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF (OLD.status='processing' AND NEW.status='completed') OR
       (OLD.status='completed' AND NEW.status='processing') THEN
      NULL;
    ELSE
      RAISE EXCEPTION 'Invalid expiry run state transition';
    END IF;
  END IF;
  IF NEW.status='completed' THEN
    SELECT count(*) FILTER (WHERE status='expired'),
           count(*) FILTER (WHERE status='skipped_already_expired'),
           count(*) FILTER (WHERE status='manual_retry_required'),
           count(*) FILTER (WHERE status NOT IN ('expired','skipped_already_expired','manual_retry_required'))
      INTO expired_items,skipped_items,manual_items,unresolved_items
      FROM bank.card_expiry_run_items
      WHERE entity_id=NEW.entity_id AND expiry_run_id=NEW.id;
    IF NEW.expired_count<>expired_items OR NEW.skipped_already_expired_count<>skipped_items OR
       NEW.manual_retry_required_count<>manual_items OR unresolved_items<>0 OR
       expired_items+skipped_items+manual_items<>NEW.item_count OR NEW.completed_at IS NULL THEN
      RAISE EXCEPTION 'Completed expiry runs require exact terminal item counts and a completion time';
    END IF;
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER guard_expiry_run BEFORE UPDATE ON bank.card_expiry_runs
  FOR EACH ROW EXECUTE FUNCTION bank.guard_expiry_run();

CREATE FUNCTION bank.guard_expiry_run_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
  IF NEW.status<>'processing' OR NEW.expired_count<>0 OR
     NEW.skipped_already_expired_count<>0 OR NEW.manual_retry_required_count<>0 OR
     NEW.completed_at IS NOT NULL THEN
    RAISE EXCEPTION 'New expiry runs must be unprocessed';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_expiry_run_insert BEFORE INSERT ON bank.card_expiry_runs
  FOR EACH ROW EXECUTE FUNCTION bank.guard_expiry_run_insert();

CREATE FUNCTION bank.guard_expiry_run_item() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE
  current_card_status bank.card_state;
  current_run_status text;
BEGIN
  IF ROW(NEW.expiry_run_id,NEW.card_id,NEW.operation_id) IS DISTINCT FROM ROW(OLD.expiry_run_id,OLD.card_id,OLD.operation_id) THEN
    RAISE EXCEPTION 'Expiry run item membership is immutable';
  END IF;
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    IF NEW.status='processing' AND
       (NEW.lease_owner IS DISTINCT FROM OLD.lease_owner OR NEW.lease_version<>OLD.lease_version) THEN
      RAISE EXCEPTION 'A processing expiry item lease owner and fencing version are immutable';
    END IF;
    IF NEW.status='processing' AND (btrim(NEW.lease_owner)='' OR NEW.lease_expires_at<=clock_timestamp()) THEN
      RAISE EXCEPTION 'A processing expiry item must retain a nonempty current lease';
    END IF;
    IF NEW.status IN ('expired','skipped_already_expired','manual_retry_required') AND
       ROW(NEW.attempt_count,NEW.automatic_retry_count,NEW.manual_retry_count,NEW.failure_code,NEW.manual_retry_by,NEW.manual_retry_at)
       IS DISTINCT FROM ROW(OLD.attempt_count,OLD.automatic_retry_count,OLD.manual_retry_count,OLD.failure_code,OLD.manual_retry_by,OLD.manual_retry_at) THEN
      RAISE EXCEPTION 'Terminal expiry item results are immutable';
    END IF;
    RETURN NEW;
  END IF;

  SELECT status INTO current_run_status
    FROM bank.card_expiry_runs
    WHERE entity_id=NEW.entity_id AND id=NEW.expiry_run_id;
  IF current_run_status IS DISTINCT FROM 'processing' THEN
    RAISE EXCEPTION 'Expiry items may change only while their run is processing';
  END IF;

  IF NEW.status='processing' THEN
    SELECT status INTO current_card_status FROM bank.cards WHERE entity_id=NEW.entity_id AND id=NEW.card_id;
    IF current_card_status='expired' THEN
      RAISE EXCEPTION 'Expired cards must be recorded as skipped_already_expired, not retried';
    END IF;
    IF OLD.status NOT IN ('pending') OR NEW.lease_version<>OLD.lease_version+1 THEN
      RAISE EXCEPTION 'Expiry item claims must start from pending and increment the fencing version';
    END IF;
  ELSIF NEW.status='pending' THEN
    IF OLD.status='processing' THEN
      IF NEW.automatic_retry_count<>OLD.automatic_retry_count+1 OR NEW.automatic_retry_count>3 OR NEW.attempt_count<>OLD.attempt_count+1 THEN
        RAISE EXCEPTION 'Automatic expiry retries must increment the attempt and retry counts';
      END IF;
    ELSIF OLD.status='manual_retry_required' THEN
      SELECT status INTO current_card_status FROM bank.cards WHERE entity_id=NEW.entity_id AND id=NEW.card_id;
      IF current_card_status='expired' THEN
        RAISE EXCEPTION 'Expired cards cannot be selected for a manual expiry retry';
      END IF;
      IF NEW.automatic_retry_count<>OLD.automatic_retry_count OR NEW.manual_retry_count<>OLD.manual_retry_count+1 OR NEW.manual_retry_by IS NULL OR NEW.manual_retry_at IS NULL THEN
        RAISE EXCEPTION 'Manual expiry retry must retain automatic retry exhaustion and record its operator';
      END IF;
    ELSE
      RAISE EXCEPTION 'Invalid expiry item state transition';
    END IF;
  ELSIF NEW.status='manual_retry_required' THEN
    IF OLD.status<>'processing' OR NEW.automatic_retry_count<>3 OR NEW.attempt_count<>OLD.attempt_count+1 THEN
      RAISE EXCEPTION 'Manual retry is required only after the third automatic retry fails';
    END IF;
  ELSIF NEW.status IN ('expired','skipped_already_expired') THEN
    IF OLD.status NOT IN ('pending','processing') THEN RAISE EXCEPTION 'Invalid expiry item state transition'; END IF;
    IF OLD.status='processing' AND NEW.attempt_count<>OLD.attempt_count+1 THEN
      RAISE EXCEPTION 'Completed expiry attempts must increment the attempt count';
    END IF;
  ELSE
    RAISE EXCEPTION 'Invalid expiry item state transition';
  END IF;

  IF NEW.status<>'processing' AND (NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL) THEN
    RAISE EXCEPTION 'Only processing expiry items may hold a lease';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER guard_expiry_run_item BEFORE UPDATE ON bank.card_expiry_run_items
  FOR EACH ROW EXECUTE FUNCTION bank.guard_expiry_run_item();

CREATE FUNCTION bank.guard_expiry_run_item_insert() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE
  run_status text;
  operation_action text;
  operation_actor_role text;
BEGIN
  SELECT status INTO run_status
    FROM bank.card_expiry_runs
    WHERE entity_id=NEW.entity_id AND id=NEW.expiry_run_id;
  IF run_status IS DISTINCT FROM 'processing' THEN
    RAISE EXCEPTION 'Expiry run items may be added only to a processing run';
  END IF;
  IF NEW.status<>'pending' OR NEW.attempt_count<>0 OR NEW.automatic_retry_count<>0 OR
     NEW.manual_retry_count<>0 OR NEW.lease_owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL OR
     NEW.lease_version<>0 OR NEW.failure_code IS NOT NULL OR NEW.manual_retry_by IS NOT NULL OR
     NEW.manual_retry_at IS NOT NULL THEN
    RAISE EXCEPTION 'New expiry run items must be unprocessed';
  END IF;

  SELECT action,actor_role INTO operation_action,operation_actor_role
    FROM bank.card_operations
    WHERE entity_id=NEW.entity_id AND card_id=NEW.card_id AND id=NEW.operation_id;
  IF operation_action IS DISTINCT FROM 'expire' OR operation_actor_role IS DISTINCT FROM 'system' THEN
    RAISE EXCEPTION 'Expiry run items require a system expire operation for the same card';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER guard_expiry_run_item_insert BEFORE INSERT ON bank.card_expiry_run_items
  FOR EACH ROW EXECUTE FUNCTION bank.guard_expiry_run_item_insert();

ALTER TABLE bank.card_expiry_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_expiry_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_expiry_runs USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_expiry_runs FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

ALTER TABLE bank.card_expiry_run_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank.card_expiry_run_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bank.card_expiry_run_items USING (entity_id=bank.current_entity_id()) WITH CHECK (entity_id=bank.current_entity_id());
CREATE TRIGGER guard_identity BEFORE UPDATE ON bank.card_expiry_run_items FOR EACH ROW EXECUTE FUNCTION bank.guard_identity();

CREATE INDEX batches_draft_review ON bank.card_status_batches(entity_id,created_at DESC,id DESC) WHERE status='draft';
CREATE INDEX batches_retry_lookup ON bank.card_status_batches(entity_id,retry_of_batch_id,created_at DESC,id DESC) WHERE retry_of_batch_id IS NOT NULL;
CREATE INDEX expiry_run_items_eligible ON bank.card_expiry_run_items(entity_id,next_attempt_at,id) WHERE status='pending';
CREATE INDEX expiry_run_items_recovery ON bank.card_expiry_run_items(entity_id,lease_expires_at,id) WHERE status='processing';
CREATE INDEX expiry_run_items_manual_retry ON bank.card_expiry_run_items(entity_id,updated_at DESC,id DESC) WHERE status='manual_retry_required';

GRANT SELECT,INSERT,UPDATE ON bank.card_expiry_runs,bank.card_expiry_run_items TO ci_business_runtime;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA bank FROM PUBLIC;
GRANT EXECUTE ON FUNCTION bank.current_entity_id() TO ci_business_runtime;
