import os
import time
import uuid
from dataclasses import dataclass
from typing import Any

import httpx
import psycopg


CARD_COUNT = 150
BATCH_REASON = "functional batch activation"
SENSITIVE_RESPONSE_FIELDS = frozenset({"pan", "cvv", "credentials", "credential_reference"})


@dataclass(frozen=True)
class Mutation:
    body: dict[str, Any]
    request_id: str
    idempotency_key: str


class API:
    def __init__(self, base_url: str) -> None:
        self.client = httpx.Client(base_url=base_url, timeout=10.0)

    def close(self) -> None:
        self.client.close()

    def request(
        self,
        method: str,
        path: str,
        *,
        token: str | None = None,
        payload: dict[str, Any] | None = None,
        expected: int = 200,
        mutation: bool = False,
        idempotency_key: str | None = None,
    ) -> Mutation | dict[str, Any]:
        request_id = str(uuid.uuid4())
        headers = {"X-Request-ID": request_id}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        key = idempotency_key or ""
        if mutation:
            if not key:
                key = f"functional-{uuid.uuid4().hex}"
            headers["Idempotency-Key"] = key
        print_endpoint(method, path)
        response = self.client.request(method, path, headers=headers, json=payload)
        assert response.status_code == expected, safe_response_body(response)
        assert response.headers["X-Request-ID"] == request_id
        assert response.headers["Content-Type"].startswith("application/json")
        body = response.json()
        assert isinstance(body, dict)
        if mutation:
            return Mutation(body, request_id, key)
        return body


def print_endpoint(method: str, path: str) -> None:
    """Emit a PCI-safe request trace without data or credentials."""
    print(f"HTTP {method} {path}")


def database_url(database: str) -> str:
    return "host={host} port={port} user={user} password={password} dbname={database}".format(
        host=os.environ["FUNCTIONAL_PGHOST"],
        port=os.environ["FUNCTIONAL_PGPORT"],
        user=os.environ["FUNCTIONAL_PGUSER"],
        password=os.environ["FUNCTIONAL_PGPASSWORD"],
        database=database,
    )


def one(connection: psycopg.Connection, statement: str, parameters: tuple[Any, ...]) -> tuple[Any, ...]:
    row = connection.execute(statement, parameters).fetchone()
    assert row is not None, "database query returned no row"
    return row


def redact(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: "[REDACTED]" if key.lower() in SENSITIVE_RESPONSE_FIELDS else redact(item) for key, item in value.items()}
    if isinstance(value, list):
        return [redact(item) for item in value]
    return value


def safe_response_body(response: httpx.Response) -> str:
    try:
        return repr(redact(response.json()))
    except ValueError:
        return "response was not JSON"


def assert_initial_credentials(body: dict[str, Any]) -> None:
    assert set(body) == {"card", "operation", "credentials"}, redact(body)
    credentials = body["credentials"]
    assert isinstance(credentials, dict), redact(body)
    assert set(credentials) == {"pan", "cvv"}, redact(body)
    pan, cvv = credentials["pan"], credentials["cvv"]
    assert isinstance(pan, str) and 13 <= len(pan) <= 19 and pan.isdecimal()
    assert isinstance(cvv, str) and len(cvv) in (3, 4) and cvv.isdecimal()


def login(api: API, username: str, password: str) -> tuple[str, str, dict[str, Any]]:
    request_id = str(uuid.uuid4())
    print_endpoint("POST", "/v1/auth/login")
    response = api.client.post(
        "/v1/auth/login",
        headers={"X-Request-ID": request_id},
        json={"username": username, "password": password},
    )
    assert response.status_code == 200, safe_response_body(response)
    assert response.headers["X-Request-ID"] == request_id
    assert response.headers["Content-Type"].startswith("application/json")
    body = response.json()
    assert isinstance(body, dict)
    token = body.get("access_token")
    assert isinstance(token, str) and token
    return token, request_id, body


def assert_login(connection: psycopg.Connection, request_id: str, user_id: str, entity_id: str | None) -> None:
    assert one(
        connection,
        "SELECT event_type, outcome, actor_user_id::text, entity_id::text, session_id IS NOT NULL, "
        "(SELECT revoked_at IS NULL FROM control.auth_sessions WHERE id=a.session_id) "
        "FROM control.authentication_audit_events a WHERE request_id=%s",
        (request_id,),
    ) == ("login", "succeeded", user_id, entity_id, True, True)


def assert_shard_idempotency(connection: psycopg.Connection, bank_id: str, scope: str, key: str, result: str) -> None:
    assert one(
        connection,
        "SELECT status, result_reference::text FROM bank.idempotency_records "
        "WHERE entity_id=%s AND operation_scope=%s AND idempotency_key=%s",
        (bank_id, scope, key),
    ) == ("succeeded", result)


def wait_for_completion(api: API, token: str, bank_id: str, batch_id: str) -> dict[str, Any]:
    deadline = time.monotonic() + 30
    latest: dict[str, Any] = {}
    path = f"/v1/banks/{bank_id}/card-status-batches/{batch_id}"
    while time.monotonic() < deadline:
        response = api.request("GET", path, token=token)
        assert isinstance(response, dict)
        latest = response
        if response.get("status") == "succeeded":
            return response
        assert response.get("status") != "failed", redact(response)
        time.sleep(0.25)
    raise AssertionError(f"batch did not complete within 30 seconds: {redact(latest)!r}")


def test_card_status_batch_activation() -> None:
    api = API(os.environ["FUNCTIONAL_BASE_URL"])
    control = psycopg.connect(database_url(os.environ["FUNCTIONAL_CONTROL_DATABASE"]))
    shard = psycopg.connect(database_url(os.environ["FUNCTIONAL_SHARD_DATABASE"]))
    run = uuid.uuid4().hex[:12]
    try:
        issuer_id = one(
            control,
            "SELECT id::text FROM control.users WHERE normalized_username='issuer_operator'",
            (),
        )[0]
        issuer_token, issuer_login_request_id, issuer_login = login(api, "issuer_operator", "Test-Issuer-Operator!2026")
        assert api.request("GET", "/v1/me", token=issuer_token) == issuer_login["user"]
        assert_login(control, issuer_login_request_id, issuer_id, None)

        bank_create = api.request(
            "POST",
            "/v1/banks",
            token=issuer_token,
            payload={"bank_reference": f"batch-functional-{run}", "name": f"Batch Functional Bank {run}"},
            expected=201,
            mutation=True,
        )
        assert isinstance(bank_create, Mutation)
        bank_id = bank_create.body["id"]
        assert api.request("GET", f"/v1/banks/{bank_id}", token=issuer_token)["id"] == bank_id
        assert one(
            control,
            "SELECT shard_id, placement_status FROM control.bank_routing_entries WHERE entity_id=%s",
            (bank_id,),
        ) == ("shard_01", "active")

        username = f"batch_functional_{run}"
        password = "Functional-Bank-Operator!2026"
        user_create = api.request(
            "POST",
            "/v1/users",
            token=issuer_token,
            payload={"username": username, "password": password, "role": "bank_operator", "entity_id": bank_id},
            expected=201,
            mutation=True,
        )
        assert isinstance(user_create, Mutation)
        bank_user_id = user_create.body["id"]
        assert api.request("GET", f"/v1/users/{bank_user_id}", token=issuer_token)["id"] == bank_user_id
        assert one(
            control,
            "SELECT normalized_username, role, entity_id::text, status, created_by::text FROM control.users WHERE id=%s",
            (bank_user_id,),
        ) == (username, "bank_operator", bank_id, "enabled", issuer_id)

        bank_token, bank_login_request_id, bank_login = login(api, username, password)
        assert api.request("GET", "/v1/me", token=bank_token) == bank_login["user"]
        assert_login(control, bank_login_request_id, bank_user_id, bank_id)

        product_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/card-products",
            token=bank_token,
            payload={"product_code": f"batch-functional-{run}", "name": "Batch Functional Product", "configuration": {}},
            expected=201,
            mutation=True,
        )
        client_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/clients",
            token=bank_token,
            payload={"external_client_ref": f"batch-client-{run}", "display_name": "Batch Functional Client"},
            expected=201,
            mutation=True,
        )
        assert isinstance(product_create, Mutation) and isinstance(client_create, Mutation)
        account_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/account-references",
            token=bank_token,
            payload={"client_id": client_create.body["id"], "external_account_ref": f"batch-account-{run}"},
            expected=201,
            mutation=True,
        )
        assert isinstance(account_create, Mutation)

        card_ids: list[str] = []
        print(f"CARD_COUNT {CARD_COUNT}")
        for index in range(CARD_COUNT):
            issue = api.request(
                "POST",
                f"/v1/banks/{bank_id}/cards",
                token=bank_token,
                payload={
                    "client_id": client_create.body["id"],
                    "account_reference_id": account_create.body["id"],
                    "product_id": product_create.body["id"],
                    "reason": f"functional batch issue {index + 1}",
                },
                expected=201,
                mutation=True,
            )
            assert isinstance(issue, Mutation)
            assert_initial_credentials(issue.body)
            card_id = issue.body["card"]["id"]
            card_ids.append(card_id)
            assert api.request("GET", f"/v1/banks/{bank_id}/cards/{card_id}", token=bank_token)["status"] == "issued"

        assert len(card_ids) == CARD_COUNT
        assert len(set(card_ids)) == CARD_COUNT
        assert one(
            shard,
            "SELECT count(*), count(*) FILTER (WHERE status='issued'), count(*) FILTER (WHERE version=1) "
            "FROM bank.cards WHERE entity_id=%s AND id=ANY(%s::uuid[])",
            (bank_id, card_ids),
        ) == (CARD_COUNT, CARD_COUNT, CARD_COUNT)

        batch_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/card-status-batches",
            token=bank_token,
            payload={"target_status": "active", "reason": BATCH_REASON, "card_ids": card_ids},
            expected=201,
            mutation=True,
        )
        assert isinstance(batch_create, Mutation)
        batch_id = batch_create.body["id"]
        assert batch_create.body["status"] == "draft"
        assert batch_create.body["target_status"] == "active"
        assert batch_create.body["reason"] == BATCH_REASON
        assert batch_create.body["item_count"] == CARD_COUNT
        assert batch_create.body["applied_count"] == 0
        assert batch_create.body["ignored_count"] == 0
        assert batch_create.body["links"]["execute"] == f"/v1/banks/{bank_id}/card-status-batches/{batch_id}:execute"
        draft_items = batch_create.body["items"]
        assert isinstance(draft_items, list) and len(draft_items) == CARD_COUNT
        assert {item["card_id"] for item in draft_items} == set(card_ids)
        assert all(item["outcome"] == "pending" and item["previous_status"] is None for item in draft_items)
        assert all(item["operation_id"] for item in draft_items)
        assert api.request("GET", f"/v1/banks/{bank_id}/card-status-batches/{batch_id}", token=bank_token)["status"] == "draft"
        draft_items_response = api.request(
            "GET",
            f"/v1/banks/{bank_id}/card-status-batches/{batch_id}/items?page_size={CARD_COUNT}",
            token=bank_token,
        )
        assert len(draft_items_response["data"]) == CARD_COUNT
        assert_shard_idempotency(
            shard,
            bank_id,
            f"card-status-batches.create.actor.{bank_user_id}",
            batch_create.idempotency_key,
            batch_id,
        )

        execute = api.request(
            "POST",
            f"/v1/banks/{bank_id}/card-status-batches/{batch_id}:execute",
            token=bank_token,
            payload={},
            expected=202,
            mutation=True,
        )
        assert isinstance(execute, Mutation)
        assert execute.body["id"] == batch_id
        assert execute.body["status"] == "queued"
        assert_shard_idempotency(
            shard,
            bank_id,
            f"card-status-batches.execute.{batch_id}.actor.{bank_user_id}",
            execute.idempotency_key,
            batch_id,
        )

        completed = wait_for_completion(api, bank_token, bank_id, batch_id)
        assert completed["id"] == batch_id
        assert completed["target_status"] == "active"
        assert completed["status"] == "succeeded"
        assert completed["item_count"] == CARD_COUNT
        assert completed["applied_count"] == CARD_COUNT
        assert completed["ignored_count"] == 0
        assert completed.get("failure_code") is None
        assert completed.get("failure_summary") is None
        assert completed["completed_at"] is not None

        completed_items_response = api.request(
            "GET",
            f"/v1/banks/{bank_id}/card-status-batches/{batch_id}/items?page_size={CARD_COUNT}",
            token=bank_token,
        )
        completed_items = completed_items_response["data"]
        assert len(completed_items) == CARD_COUNT
        assert {item["card_id"] for item in completed_items} == set(card_ids)
        assert all(item["outcome"] == "applied" and item["previous_status"] == "issued" for item in completed_items)
        assert all(item["failure_code"] is None for item in completed_items)
        for card_id in card_ids:
            assert api.request("GET", f"/v1/banks/{bank_id}/cards/{card_id}", token=bank_token)["status"] == "active"

        assert one(
            shard,
            "SELECT target_status::text, reason, requested_by::text, requester_role, requester_entity_id::text, status::text, "
            "item_count, applied_count, ignored_count, failure_code IS NULL, failure_summary IS NULL, completed_at IS NOT NULL "
            "FROM bank.card_status_batches WHERE entity_id=%s AND id=%s",
            (bank_id, batch_id),
        ) == ("active", BATCH_REASON, bank_user_id, "bank_operator", bank_id, "succeeded", CARD_COUNT, CARD_COUNT, 0, True, True, True)

        card_rows = shard.execute(
            "SELECT id::text, status::text, version, activated_at IS NOT NULL FROM bank.cards "
            "WHERE entity_id=%s AND id=ANY(%s::uuid[]) ORDER BY id",
            (bank_id, card_ids),
        ).fetchall()
        assert len(card_rows) == CARD_COUNT
        assert {row[0] for row in card_rows} == set(card_ids)
        assert all(row[1:] == ("active", 2, True) for row in card_rows)

        operation_rows = shard.execute(
            "SELECT i.card_id::text, i.outcome, i.previous_status::text, i.failure_code, o.action, o.status, o.reason, "
            "o.actor_user_id::text, o.actor_role, o.actor_entity_id::text, o.request_id::text, o.completed_at IS NOT NULL, o.executor_identity IS NOT NULL "
            "FROM bank.card_status_batch_items i JOIN bank.card_operations o ON o.entity_id=i.entity_id AND o.id=i.operation_id "
            "WHERE i.entity_id=%s AND i.batch_id=%s",
            (bank_id, batch_id),
        ).fetchall()
        assert len(operation_rows) == CARD_COUNT
        assert {row[0] for row in operation_rows} == set(card_ids)
        assert all(
            row[1:] == ("applied", "issued", None, "activate", "succeeded", BATCH_REASON, bank_user_id, "bank_operator", bank_id, batch_create.request_id, True, True)
            for row in operation_rows
        )

        history_rows = shard.execute(
            "SELECT h.card_id::text, h.previous_status::text, h.new_status::text, h.reason, h.actor_user_id::text, h.actor_role, h.actor_entity_id::text "
            "FROM bank.card_status_history h JOIN bank.card_status_batch_items i ON i.entity_id=h.entity_id AND i.operation_id=h.operation_id "
            "WHERE h.entity_id=%s AND i.batch_id=%s",
            (bank_id, batch_id),
        ).fetchall()
        assert len(history_rows) == CARD_COUNT
        assert {row[0] for row in history_rows} == set(card_ids)
        assert all(row[1:] == ("issued", "active", BATCH_REASON, bank_user_id, "bank_operator", bank_id) for row in history_rows)

        assert one(
            shard,
            "SELECT action, resource_type, resource_id::text, outcome FROM bank.audit_events "
            "WHERE entity_id=%s AND request_id=%s AND action='card_status_batch.create'",
            (bank_id, batch_create.request_id),
        ) == ("card_status_batch.create", "card_status_batch", batch_id, "succeeded")
        assert one(
            shard,
            "SELECT action, resource_type, resource_id::text, outcome FROM bank.audit_events "
            "WHERE entity_id=%s AND request_id=%s AND action='card_status_batch.execute'",
            (bank_id, execute.request_id),
        ) == ("card_status_batch.execute", "card_status_batch", batch_id, "succeeded")
        apply_audits = shard.execute(
            "SELECT resource_id::text, actor_user_id::text, actor_role, actor_entity_id::text, executor_identity IS NOT NULL "
            "FROM bank.audit_events WHERE entity_id=%s AND request_id=%s AND action='batch.apply' AND resource_type='card'",
            (bank_id, batch_create.request_id),
        ).fetchall()
        assert len(apply_audits) == CARD_COUNT
        assert {row[0] for row in apply_audits} == set(card_ids)
        assert all(row[1:] == (bank_user_id, "bank_operator", bank_id, True) for row in apply_audits)
    finally:
        api.close()
        control.close()
        shard.close()
