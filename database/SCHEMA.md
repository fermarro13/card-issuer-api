# Initial schema reference

The two `001_initial.sql` migrations are the source of truth for nullability, defaults, checks, keys and indexes. This reference lists their concrete columns. UUIDs are opaque identifiers; event/lifecycle timestamps use `timestamptz`.

## Conventions

- Control tables use UUID primary keys and local foreign keys. Bank children use `(entity_id,id)` keys; Entity uses `id`.
- Mutable records have creation/update timestamps and actor UUIDs; shard actors refer logically to central users without cross-database foreign keys.
- Required creation attribution is immutable. Update timestamps advance automatically; callers supply the acting user in `updated_by`. Business card/configuration version increments are application responsibilities.
- Text codes/references are opaque, case-sensitive and nonempty unless noted by the DDL. No account balances, PAN, CVV, raw tokens, or customer identity matching are modeled.
- `bank.card_state`: pending, issued, active, suspended, closed, expired. `bank.execution_state`: queued, processing, succeeded, failed.
- JSONB fields accept objects. Field allowlists, size bounds and sanitization are application responsibilities; Client has only optional display name beyond its bank reference.
- Runtime audit/history access is append-only. Retention/cleanup roles and jobs are not provisioned.
- `ci_meta.database_identity` stores the database kind; `ci_meta.schema_migrations` stores version, filename, checksum and applied time.

## `control.bank_routing_entries`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid PRIMARY KEY` |
| `shard_id` | `text NOT NULL CHECK (btrim(shard_id) <> '')` |
| `routing_version` | `bigint NOT NULL DEFAULT 1 CHECK (routing_version > 0)` |
| `placement_status` | `text NOT NULL CHECK (placement_status IN ('provisioning','active','paused'))` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |

## `control.users`

| Column | SQL definition |
| --- | --- |
| `id` | `uuid PRIMARY KEY DEFAULT gen_random_uuid()` |
| `normalized_username` | `text NOT NULL UNIQUE CHECK (normalized_username COLLATE "C" ~ '^[a-z0-9][a-z0-9_.@-]{0,127}$')` |
| `password_hash` | `text NOT NULL CHECK (password_hash LIKE '$argon2id$%')` |
| `role` | `text NOT NULL CHECK (role IN ('issuer_operator','issuer_readonly','bank_operator','bank_readonly'))` |
| `entity_id` | `uuid REFERENCES control.bank_routing_entries(entity_id)` |
| `status` | `text NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled','disabled'))` |
| `auth_version` | `bigint NOT NULL DEFAULT 1 CHECK (auth_version > 0)` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid REFERENCES control.users(id)` |
| `updated_by` | `uuid REFERENCES control.users(id)` |

## `control.auth_sessions`

| Column | SQL definition |
| --- | --- |
| `id` | `uuid PRIMARY KEY DEFAULT gen_random_uuid()` |
| `user_id` | `uuid NOT NULL REFERENCES control.users(id)` |
| `auth_version` | `bigint NOT NULL CHECK (auth_version > 0)` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `expires_at` | `timestamptz NOT NULL DEFAULT (now() + interval '7 days')` |
| `last_refreshed_at` | `timestamptz NOT NULL DEFAULT now()` |
| `revoked_at` | `timestamptz` |
| `revocation_reason` | `text` |

## `control.refresh_tokens`

| Column | SQL definition |
| --- | --- |
| `id` | `uuid PRIMARY KEY DEFAULT gen_random_uuid()` |
| `session_id` | `uuid NOT NULL REFERENCES control.auth_sessions(id)` |
| `token_hash` | `bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32)` |
| `parent_token_id` | `uuid` |
| `issued_at` | `timestamptz NOT NULL DEFAULT now()` |
| `expires_at` | `timestamptz NOT NULL` |
| `consumed_at` | `timestamptz` |

## `control.authentication_audit_events`

| Column | SQL definition |
| --- | --- |
| `id` | `uuid PRIMARY KEY DEFAULT gen_random_uuid()` |
| `event_type` | `text NOT NULL CHECK (btrim(event_type)<>'')` |
| `outcome` | `text NOT NULL CHECK (outcome IN ('succeeded','failed','rejected'))` |
| `actor_user_id` | `uuid REFERENCES control.users(id)` |
| `subject_user_id` | `uuid REFERENCES control.users(id)` |
| `entity_id` | `uuid REFERENCES control.bank_routing_entries(entity_id)` |
| `session_id` | `uuid REFERENCES control.auth_sessions(id)` |
| `request_id` | `uuid NOT NULL` |
| `occurred_at` | `timestamptz NOT NULL DEFAULT now()` |
| `details` | `jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details)='object')` |

## `bank.entities`

| Column | SQL definition |
| --- | --- |
| `id` | `uuid PRIMARY KEY DEFAULT gen_random_uuid()` |
| `bank_reference` | `text NOT NULL UNIQUE CHECK (btrim(bank_reference)<>'')` |
| `name` | `text NOT NULL CHECK (btrim(name)<>'')` |
| `status` | `text NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive'))` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.clients`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `external_client_ref` | `text NOT NULL CHECK (btrim(external_client_ref)<>'')` |
| `display_name` | `text` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.account_references`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `client_id` | `uuid NOT NULL` |
| `external_account_ref` | `text NOT NULL CHECK (btrim(external_account_ref)<>'')` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.card_products`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `product_code` | `text NOT NULL CHECK (btrim(product_code)<>'')` |
| `name` | `text NOT NULL CHECK (btrim(name)<>'')` |
| `status` | `text NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive'))` |
| `configuration` | `jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(configuration)='object')` |
| `configuration_version` | `bigint NOT NULL DEFAULT 1 CHECK (configuration_version>0)` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.cards`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `client_id` | `uuid NOT NULL` |
| `account_reference_id` | `uuid NOT NULL` |
| `product_id` | `uuid NOT NULL` |
| `status` | `bank.card_state NOT NULL DEFAULT 'pending'` |
| `credential_reference` | `text CHECK (credential_reference IS NULL OR btrim(credential_reference)<>'')` |
| `predecessor_card_id` | `uuid` |
| `issued_at` | `timestamptz` |
| `activated_at` | `timestamptz` |
| `suspended_at` | `timestamptz` |
| `closed_at` | `timestamptz` |
| `expires_at` | `timestamptz` |
| `version` | `bigint NOT NULL DEFAULT 1 CHECK (version>0)` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.card_operations`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `card_id` | `uuid NOT NULL` |
| `action` | `text NOT NULL CHECK (action IN ('issue','activate','suspend','resume','close','expire','replace'))` |
| `status` | `bank.execution_state NOT NULL DEFAULT 'queued'` |
| `reason` | `text NOT NULL CHECK (btrim(reason)<>'')` |
| `actor_user_id` | `uuid NOT NULL` |
| `actor_role` | `text NOT NULL CHECK (actor_role IN ('issuer_operator','bank_operator'))` |
| `actor_entity_id` | `uuid` |
| `executor_identity` | `text` |
| `request_id` | `uuid NOT NULL` |
| `started_at` | `timestamptz` |
| `completed_at` | `timestamptz` |
| `failure_code` | `text` |
| `failure_summary` | `text` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.card_status_history`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `card_id` | `uuid NOT NULL` |
| `operation_id` | `uuid NOT NULL` |
| `previous_status` | `bank.card_state NOT NULL` |
| `new_status` | `bank.card_state NOT NULL` |
| `reason` | `text NOT NULL CHECK (btrim(reason)<>'')` |
| `actor_user_id` | `uuid NOT NULL` |
| `actor_role` | `text NOT NULL CHECK (actor_role IN ('issuer_operator','bank_operator'))` |
| `actor_entity_id` | `uuid` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |

## `bank.audit_events`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `actor_user_id` | `uuid NOT NULL` |
| `actor_role` | `text NOT NULL CHECK (actor_role IN ('issuer_operator','issuer_readonly','bank_operator','bank_readonly'))` |
| `actor_entity_id` | `uuid` |
| `executor_identity` | `text` |
| `action` | `text NOT NULL CHECK (btrim(action)<>'')` |
| `resource_type` | `text NOT NULL CHECK (btrim(resource_type)<>'')` |
| `resource_id` | `uuid` |
| `outcome` | `text NOT NULL CHECK (outcome IN ('succeeded','failed','rejected'))` |
| `request_id` | `uuid NOT NULL` |
| `occurred_at` | `timestamptz NOT NULL DEFAULT now()` |
| `details` | `jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(details)='object')` |

## `bank.idempotency_records`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `operation_scope` | `text NOT NULL CHECK (btrim(operation_scope)<>'')` |
| `idempotency_key` | `text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 256)` |
| `request_fingerprint` | `bytea NOT NULL CHECK (octet_length(request_fingerprint)=32)` |
| `status` | `text NOT NULL DEFAULT 'processing' CHECK (status IN ('processing','succeeded','failed'))` |
| `result_reference` | `uuid` |
| `expires_at` | `timestamptz NOT NULL` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.card_status_batches`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `target_status` | `bank.card_state NOT NULL` |
| `reason` | `text NOT NULL CHECK (btrim(reason)<>'')` |
| `requested_by` | `uuid NOT NULL` |
| `requester_role` | `text NOT NULL CHECK (requester_role IN ('issuer_operator','bank_operator'))` |
| `requester_entity_id` | `uuid` |
| `request_id` | `uuid NOT NULL` |
| `idempotency_record_id` | `uuid NOT NULL` |
| `status` | `bank.execution_state NOT NULL DEFAULT 'queued'` |
| `item_count` | `integer NOT NULL CHECK (item_count>0)` |
| `attempt_count` | `integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0)` |
| `next_attempt_at` | `timestamptz NOT NULL DEFAULT now()` |
| `lease_owner` | `text` |
| `lease_expires_at` | `timestamptz` |
| `lease_version` | `bigint NOT NULL DEFAULT 0 CHECK (lease_version>=0)` |
| `started_at` | `timestamptz` |
| `completed_at` | `timestamptz` |
| `failure_code` | `text` |
| `failure_summary` | `text` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## `bank.card_status_batch_items`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `batch_id` | `uuid NOT NULL` |
| `card_id` | `uuid NOT NULL` |
| `operation_id` | `uuid NOT NULL` |
| `previous_status` | `bank.card_state` |
| `outcome` | `text NOT NULL DEFAULT 'pending' CHECK (outcome IN ('pending','applied','not_applied'))` |
| `failure_code` | `text` |

## `bank.outbox_messages`

| Column | SQL definition |
| --- | --- |
| `entity_id` | `uuid NOT NULL REFERENCES bank.entities(id)` |
| `id` | `uuid NOT NULL DEFAULT gen_random_uuid()` |
| `work_type` | `text NOT NULL CHECK (btrim(work_type)<>'')` |
| `operation_id` | `uuid` |
| `batch_id` | `uuid` |
| `payload` | `jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(payload)='object')` |
| `status` | `text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered','failed'))` |
| `attempt_count` | `integer NOT NULL DEFAULT 0 CHECK (attempt_count>=0)` |
| `next_attempt_at` | `timestamptz NOT NULL DEFAULT now()` |
| `lease_owner` | `text` |
| `lease_expires_at` | `timestamptz` |
| `lease_version` | `bigint NOT NULL DEFAULT 0 CHECK (lease_version>=0)` |
| `delivered_at` | `timestamptz` |
| `failure_code` | `text` |
| `created_at` | `timestamptz NOT NULL DEFAULT now()` |
| `updated_at` | `timestamptz NOT NULL DEFAULT now()` |
| `created_by` | `uuid NOT NULL` |
| `updated_by` | `uuid NOT NULL` |

## Enforced relationships and boundaries

Cards reference an account with the same bank and client. History and batch items reference an operation for the same bank and card. Existing batch item membership and accepted batch/operation request attribution cannot be rewritten. Each operation appears in at most one batch item; a batch cannot repeat a card.

Bank users require a central directory assignment; issuer users require no assignment. Usernames normalize to trimmed lowercase ASCII before uniqueness checks. Sensitive user changes advance authorization versions. Session identity/absolute expiry and token identity/ancestry/lifetime are immutable; revoked sessions and consumed-token evidence cannot be restored. Token parents must already exist in the same family, preventing cycles and cross-family links; unique parent references prevent branching. Token insertions lock the session and enforce its lifetime.

The schema does not authenticate a request or execute lifecycle/refresh/batch workflows. Submission membership completeness, valid card transitions, audit co-commit, delivery semantics, refresh replay handling, session/user authorization checks and cross-database routing authorization remain application responsibilities described in the README.

