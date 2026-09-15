ALTER TABLE bank.card_status_batches ADD CONSTRAINT batch_executor_failure_code_check
  CHECK (failure_code IS NULL OR failure_code IN ('authorization_revoked','retry_exhausted','invalid_transition','control_unavailable','dependency_unavailable'));
ALTER TABLE bank.card_status_batch_items ADD CONSTRAINT batch_item_executor_failure_code_check
  CHECK (failure_code IS NULL OR failure_code IN ('authorization_revoked','retry_exhausted','invalid_transition','control_unavailable','dependency_unavailable'));
ALTER TABLE bank.card_expiry_run_items ADD CONSTRAINT expiry_item_executor_failure_code_check
  CHECK (failure_code IS NULL OR failure_code IN ('expiry_failed'));
ALTER TABLE bank.card_status_batches ADD CONSTRAINT batch_executor_failure_summary_check
  CHECK (failure_summary IS NULL OR failure_summary IN ('The requester is no longer authorized.','The retry limit was reached.','One or more card transitions are not allowed.','Authorization dependency is temporarily unavailable.','A temporary dependency failure occurred.','The batch will be retried.','The batch could not be completed.'));

GRANT USAGE ON SCHEMA bank TO ci_executor_runtime;
GRANT SELECT (entity_id,id,target_status,reason,requested_by,requester_role,requester_entity_id,request_id,status,item_count,attempt_count,next_attempt_at,lease_owner,lease_expires_at,lease_version,started_at), UPDATE (status,lease_owner,lease_expires_at,lease_version,attempt_count,next_attempt_at,started_at,completed_at,failure_code,failure_summary,applied_count,ignored_count,updated_at) ON bank.card_status_batches TO ci_executor_runtime;
GRANT SELECT (entity_id,id,batch_id,card_id,operation_id,outcome), UPDATE (outcome,previous_status,failure_code) ON bank.card_status_batch_items TO ci_executor_runtime;
GRANT SELECT (entity_id,id,status,expires_at,activated_at,suspended_at,closed_at,version), UPDATE (status,activated_at,suspended_at,closed_at,expires_at,version,updated_by,updated_at) ON bank.cards TO ci_executor_runtime;
GRANT SELECT (entity_id,id,card_id,status), INSERT (entity_id,card_id,action,status,reason,actor_role,executor_identity,request_id,started_at,created_by,updated_by), UPDATE (status,executor_identity,started_at,completed_at,updated_by,updated_at,failure_code,failure_summary) ON bank.card_operations TO ci_executor_runtime;
GRANT INSERT (entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id,executor_identity) ON bank.card_status_history TO ci_executor_runtime;
GRANT INSERT (entity_id,actor_user_id,actor_role,actor_entity_id,executor_identity,action,resource_type,resource_id,outcome,request_id,details) ON bank.audit_events TO ci_executor_runtime;
GRANT SELECT (entity_id,id,run_date,status,updated_at), INSERT (entity_id,run_date,executor_identity), UPDATE (item_count,expired_count,skipped_already_expired_count,manual_retry_required_count,status,completed_at,updated_at) ON bank.card_expiry_runs TO ci_executor_runtime;
GRANT SELECT (entity_id,id,expiry_run_id,card_id,operation_id,status,attempt_count,automatic_retry_count,next_attempt_at,lease_owner,lease_expires_at,lease_version), INSERT (entity_id,expiry_run_id,card_id,operation_id), UPDATE (status,attempt_count,automatic_retry_count,next_attempt_at,lease_owner,lease_expires_at,lease_version,failure_code,updated_at) ON bank.card_expiry_run_items TO ci_executor_runtime;
GRANT EXECUTE ON FUNCTION bank.current_entity_id() TO ci_executor_runtime;
