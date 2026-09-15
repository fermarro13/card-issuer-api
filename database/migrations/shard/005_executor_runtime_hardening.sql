-- The original system-actor migration replaced the named actor-context check
-- but left the older unnamed equivalent in place. Remove only that obsolete
-- guard, then grant the two columns read by the expiry-item insert trigger.
ALTER TABLE bank.card_status_history DROP CONSTRAINT card_status_history_check1;
GRANT SELECT (action,actor_role) ON bank.card_operations TO ci_executor_runtime;
