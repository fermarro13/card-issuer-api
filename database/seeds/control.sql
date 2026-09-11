SET ROLE ci_owner;
BEGIN;
SELECT pg_advisory_xact_lock(170017,3);
CREATE TEMP TABLE fixture_placement (shard_id text NOT NULL) ON COMMIT DROP;
INSERT INTO fixture_placement VALUES (:'shard_id');
INSERT INTO control.bank_routing_entries(entity_id,shard_id,placement_status)
VALUES ('10000000-0000-4000-8000-000000000001',:'shard_id','active')
ON CONFLICT (entity_id) DO NOTHING;
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM control.bank_routing_entries
                 WHERE entity_id='10000000-0000-4000-8000-000000000001'
                   AND shard_id=(SELECT shard_id FROM fixture_placement)) THEN
    RAISE EXCEPTION 'Test bank is already assigned to another shard';
  END IF;
END $$;
-- Only SetupTest orchestrates this after shard provisioning commits.
UPDATE control.bank_routing_entries SET placement_status='active',updated_at=clock_timestamp()
WHERE entity_id='10000000-0000-4000-8000-000000000001' AND placement_status='provisioning';

CREATE TEMP TABLE fixture_users (id uuid,username text,password_hash text,role text,entity_id uuid) ON COMMIT DROP;
INSERT INTO fixture_users VALUES
('20000000-0000-4000-8000-000000000001','issuer_operator','$argon2id$v=19$m=19456,t=2,p=1$a5cSmLw26Ij1Grx1duyRbg$4TkbMQuIpUQA9uLWupb1Hz87cmt/Na1GIAD9hf97qWk','issuer_operator',NULL),
('20000000-0000-4000-8000-000000000002','issuer_readonly','$argon2id$v=19$m=19456,t=2,p=1$390C1wxkcpwZgQBlodoCdA$I1dQeKnqOVmLKJ86Y/ogYxo8M9Q7Uw4dPlNiDL9wwok','issuer_readonly',NULL),
('20000000-0000-4000-8000-000000000003','bank_operator','$argon2id$v=19$m=19456,t=2,p=1$TFRK7SwQktPc5kJqDbjHOw$kOTW7pnbUltd0dK3tCr/mpaFvA96vh9ziLQScKWB5X0','bank_operator','10000000-0000-4000-8000-000000000001'),
('20000000-0000-4000-8000-000000000004','bank_readonly','$argon2id$v=19$m=19456,t=2,p=1$l+pS4vietfTDGPDNxvwRHw$bUD68jJAA3BRr4+mu/ye9W8kbDG2VPA7B8kKdpjz0mo','bank_readonly','10000000-0000-4000-8000-000000000001');
DO $$ BEGIN
  IF EXISTS (SELECT FROM fixture_users f JOIN control.users u ON u.id=f.id OR u.normalized_username=f.username
             WHERE u.id<>f.id OR u.normalized_username<>f.username) THEN
    RAISE EXCEPTION 'Test user identity collision';
  END IF;
END $$;
-- Existing identities may have intentionally changed password, permissions or status.
WITH inserted AS (
  INSERT INTO control.users(id,normalized_username,password_hash,role,entity_id,created_by,updated_by)
  SELECT id,username,password_hash,role,entity_id,
         CASE WHEN role='issuer_operator' THEN NULL ELSE '20000000-0000-4000-8000-000000000001'::uuid END,
         CASE WHEN role='issuer_operator' THEN NULL ELSE '20000000-0000-4000-8000-000000000001'::uuid END
  FROM fixture_users ON CONFLICT (id) DO NOTHING RETURNING id,entity_id
)
INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,entity_id,request_id,details)
SELECT 'test_user_provisioned','succeeded',NULL,id,entity_id,gen_random_uuid(),'{"source":"test_seed"}' FROM inserted;
COMMIT;
