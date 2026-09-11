-- All checks below reuse one connection and execute as actual runtime/owner roles.
CREATE FUNCTION pg_temp.assert_true(value boolean, message text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN IF value IS DISTINCT FROM true THEN RAISE EXCEPTION 'Assertion failed: %',message; END IF; END $$;
SET ROLE ci_business_runtime;
SELECT pg_temp.assert_true((SELECT count(*)=0 FROM bank.clients),'missing tenant context');
BEGIN;
SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000001';
SELECT pg_temp.assert_true((SELECT count(*)=2 FROM bank.clients),'first bank only');
SELECT pg_temp.assert_true((SELECT count(*)=1 FROM bank.entities),'entity table isolated');
SELECT pg_temp.assert_true((SELECT count(*)=0 FROM bank.clients WHERE id='30000000-0000-4000-8000-000000000003'),'guessed cross-bank ID');
SELECT pg_temp.assert_true((SELECT count(*)=1 FROM bank.clients c JOIN bank.account_references a ON c.entity_id=a.entity_id AND c.id=a.client_id),'tenant-scoped join');
COMMIT;
SELECT pg_temp.assert_true((SELECT count(*)=0 FROM bank.clients),'tenant cleared after commit');
BEGIN;
SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000002';
SELECT pg_temp.assert_true((SELECT count(*)=1 FROM bank.clients),'second bank only');
ROLLBACK;
SELECT pg_temp.assert_true((SELECT count(*)=0 FROM bank.clients),'tenant cleared after rollback');
RESET ROLE;
SET ROLE ci_owner;
SELECT pg_temp.assert_true((SELECT count(*)=0 FROM bank.clients),'FORCE RLS applies to owner');
RESET ROLE;
