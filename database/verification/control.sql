-- Run only on disposable test databases after SetupTest. Every change rolls back.
BEGIN;
CREATE FUNCTION pg_temp.assert_true(value boolean, message text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN IF value IS DISTINCT FROM true THEN RAISE EXCEPTION 'Assertion failed: %',message; END IF; END $$;
CREATE FUNCTION pg_temp.expect_error(command text, expected_state text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE actual text;
BEGIN
  BEGIN EXECUTE command;
  EXCEPTION WHEN OTHERS THEN
    GET STACKED DIAGNOSTICS actual=RETURNED_SQLSTATE;
    IF actual<>expected_state THEN RAISE EXCEPTION 'Expected %, got %',expected_state,actual; END IF;
    RETURN;
  END;
  RAISE EXCEPTION 'Expected SQLSTATE %, command succeeded',expected_state;
END $$;
SET LOCAL ROLE ci_auth_runtime;
INSERT INTO control.users(id,normalized_username,password_hash,role)
SELECT '21000000-0000-4000-8000-000000000001','  Mixed.Case  ',password_hash,'issuer_readonly' FROM control.users WHERE normalized_username='issuer_operator';
SELECT pg_temp.assert_true((SELECT normalized_username='mixed.case' FROM control.users WHERE id='21000000-0000-4000-8000-000000000001'),'username normalization');
SELECT pg_temp.expect_error($q$INSERT INTO control.users(normalized_username,password_hash,role) SELECT 'MIXED.CASE',password_hash,'issuer_readonly' FROM control.users LIMIT 1$q$,'23505');
SELECT pg_temp.expect_error($q$UPDATE control.users SET role='bank_operator' WHERE normalized_username='mixed.case'$q$,'23514');
SELECT pg_temp.expect_error($q$UPDATE control.users SET entity_id='10000000-0000-4000-8000-000000000001' WHERE normalized_username='mixed.case'$q$,'23514');
SELECT pg_temp.expect_error($q$UPDATE control.users SET role='superuser' WHERE normalized_username='mixed.case'$q$,'23514');
SELECT pg_temp.expect_error($q$UPDATE control.users SET normalized_username='nönascii' WHERE normalized_username='mixed.case'$q$,'23514');
DO $$ DECLARE v bigint; BEGIN
  SELECT auth_version INTO v FROM control.users WHERE normalized_username='mixed.case';
  UPDATE control.users SET status='disabled',auth_version=v WHERE normalized_username='mixed.case';
  PERFORM pg_temp.assert_true((SELECT auth_version=v+1 FROM control.users WHERE normalized_username='mixed.case'),'disable bumps version');
  UPDATE control.users SET status='enabled' WHERE normalized_username='mixed.case';
  UPDATE control.users SET role='issuer_operator' WHERE normalized_username='mixed.case';
  UPDATE control.users SET password_hash=password_hash||'x' WHERE normalized_username='mixed.case';
  UPDATE control.users SET role='bank_operator',entity_id='10000000-0000-4000-8000-000000000001' WHERE normalized_username='mixed.case';
  PERFORM pg_temp.assert_true((SELECT auth_version=v+5 FROM control.users WHERE normalized_username='mixed.case'),'sensitive changes bump version');
END $$;
SELECT pg_temp.expect_error($q$UPDATE control.users SET auth_version=1 WHERE normalized_username='mixed.case'$q$,'P0001');
INSERT INTO control.auth_sessions(id,user_id,auth_version) VALUES
('22000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001',1),
('22000000-0000-4000-8000-000000000002','20000000-0000-4000-8000-000000000001',1);
INSERT INTO control.refresh_tokens(id,session_id,token_hash,expires_at) VALUES
('23000000-0000-4000-8000-000000000001','22000000-0000-4000-8000-000000000001',decode(repeat('01',32),'hex'),now()+interval '7 days'),
('23000000-0000-4000-8000-000000000002','22000000-0000-4000-8000-000000000002',decode(repeat('02',32),'hex'),now()+interval '7 days');
SELECT pg_temp.expect_error($q$INSERT INTO control.refresh_tokens(session_id,token_hash,parent_token_id,expires_at) VALUES ('22000000-0000-4000-8000-000000000001',decode(repeat('03',32),'hex'),'23000000-0000-4000-8000-000000000002',now()+interval '1 day')$q$,'23503');
SELECT pg_temp.expect_error($q$INSERT INTO control.refresh_tokens(session_id,token_hash,parent_token_id,expires_at) VALUES ('22000000-0000-4000-8000-000000000001',decode(repeat('03',32),'hex'),'23000000-0000-4000-8000-000000000001',now()+interval '8 days')$q$,'P0001');
UPDATE control.refresh_tokens SET consumed_at=now() WHERE id='23000000-0000-4000-8000-000000000001';
INSERT INTO control.refresh_tokens(id,session_id,token_hash,parent_token_id,expires_at) VALUES
('23000000-0000-4000-8000-000000000003','22000000-0000-4000-8000-000000000001',decode(repeat('03',32),'hex'),'23000000-0000-4000-8000-000000000001',now()+interval '7 days');
SELECT pg_temp.expect_error($q$INSERT INTO control.refresh_tokens(session_id,token_hash,parent_token_id,expires_at) VALUES ('22000000-0000-4000-8000-000000000001',decode(repeat('04',32),'hex'),'23000000-0000-4000-8000-000000000001',now()+interval '7 days')$q$,'23505');
SELECT pg_temp.expect_error($q$INSERT INTO control.refresh_tokens(session_id,token_hash,parent_token_id,expires_at) VALUES ('22000000-0000-4000-8000-000000000001',decode(repeat('03',32),'hex'),'23000000-0000-4000-8000-000000000003',now()+interval '7 days')$q$,'23505');
SELECT pg_temp.expect_error($q$UPDATE control.refresh_tokens SET parent_token_id=NULL WHERE id='23000000-0000-4000-8000-000000000003'$q$,'P0001');
SELECT pg_temp.expect_error($q$UPDATE control.refresh_tokens SET consumed_at=NULL WHERE id='23000000-0000-4000-8000-000000000001'$q$,'P0001');
SELECT pg_temp.expect_error($q$UPDATE control.auth_sessions SET expires_at=expires_at-interval '1 day'$q$,'P0001');
UPDATE control.auth_sessions SET revoked_at=now(),revocation_reason='test' WHERE id='22000000-0000-4000-8000-000000000001';
SELECT pg_temp.expect_error($q$UPDATE control.auth_sessions SET revoked_at=NULL,revocation_reason=NULL WHERE id='22000000-0000-4000-8000-000000000001'$q$,'P0001');
SELECT pg_temp.expect_error($q$INSERT INTO control.refresh_tokens(session_id,token_hash,parent_token_id,expires_at) VALUES ('22000000-0000-4000-8000-000000000001',decode(repeat('04',32),'hex'),'23000000-0000-4000-8000-000000000003',now()+interval '7 days')$q$,'P0001');
SELECT pg_temp.expect_error('DELETE FROM control.authentication_audit_events','42501');
SELECT pg_temp.expect_error('TRUNCATE control.authentication_audit_events','42501');
SELECT pg_temp.expect_error('DELETE FROM control.refresh_tokens','42501');
ROLLBACK;
