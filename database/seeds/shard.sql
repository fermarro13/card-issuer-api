SET ROLE ci_owner;
BEGIN;
SELECT pg_advisory_xact_lock(170017,3);
SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000001';
WITH inserted AS (
INSERT INTO bank.entities(id,bank_reference,name,created_by,updated_by)
VALUES ('10000000-0000-4000-8000-000000000001','TEST_BANK','Test Bank',
        '20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001')
ON CONFLICT (id) DO NOTHING RETURNING id
)
INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id,details)
SELECT id,'20000000-0000-4000-8000-000000000001','issuer_operator','test_bank_provisioned','entity',id,'succeeded',gen_random_uuid(),'{"source":"test_seed"}' FROM inserted;
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM bank.entities WHERE id='10000000-0000-4000-8000-000000000001' AND bank_reference='TEST_BANK') THEN
    RAISE EXCEPTION 'Test bank identity collision';
  END IF;
END $$;
COMMIT;
