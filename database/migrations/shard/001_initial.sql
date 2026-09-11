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
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA bank FROM PUBLIC;
GRANT EXECUTE ON FUNCTION bank.current_entity_id() TO ci_business_runtime;
