GRANT USAGE ON SCHEMA control TO ci_executor_runtime;
GRANT SELECT (id,normalized_username,role,entity_id,status,created_at,updated_at) ON control.users TO ci_executor_runtime;
GRANT SELECT (entity_id,shard_id,placement_status,created_at) ON control.bank_routing_entries TO ci_executor_runtime;
REVOKE ALL ON control.auth_sessions,control.refresh_tokens,control.authentication_audit_events,control.command_idempotency_records FROM ci_executor_runtime;
