import os
import uuid
from dataclasses import dataclass
from typing import Any

import httpx
import psycopg


SENSITIVE_RESPONSE_FIELDS = frozenset({"pan", "cvv", "credentials", "credential_reference"})


@dataclass(frozen=True)
class Mutation:
    body: dict[str, Any]
    request_id: str
    idempotency_key: str


@dataclass(frozen=True)
class TenantSnapshot:
    card_products: int
    clients: int
    account_references: int
    cards: int
    card_operations: int
    card_status_history: int
    card_status_batches: int
    card_status_batch_items: int
    audit_events: int
    idempotency_records: int
    card_status: str
    card_version: int
    batch_status: str


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
        token: str,
        payload: dict[str, Any] | None = None,
        expected: int = 200,
        mutation: bool = False,
    ) -> Mutation | dict[str, Any]:
        request_id = str(uuid.uuid4())
        headers = {"Authorization": f"Bearer {token}", "X-Request-ID": request_id}
        idempotency_key = ""
        if mutation:
            idempotency_key = f"functional-isolation-{uuid.uuid4().hex}"
            headers["Idempotency-Key"] = idempotency_key
        print_endpoint(method, path)
        response = self.client.request(method, path, headers=headers, json=payload)
        assert response.status_code == expected, safe_response_body(response)
        assert response.headers["X-Request-ID"] == request_id
        assert response.headers["Content-Type"].startswith("application/json")
        body = response.json()
        assert isinstance(body, dict)
        if mutation:
            return Mutation(body, request_id, idempotency_key)
        return body


def print_endpoint(method: str, path: str) -> None:
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


def assert_no_sensitive_fields(value: Any) -> None:
    if isinstance(value, dict):
        assert not (set(value) & SENSITIVE_RESPONSE_FIELDS), redact(value)
        for item in value.values():
            assert_no_sensitive_fields(item)
    elif isinstance(value, list):
        for item in value:
            assert_no_sensitive_fields(item)


def login(api: API, username: str, password: str) -> str:
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
    return token


def create_bank(api: API, issuer_token: str, run: str, label: str) -> str:
    result = api.request(
        "POST",
        "/v1/banks",
        token=issuer_token,
        payload={"bank_reference": f"tenant-isolation-{label.lower()}-{run}", "name": f"Tenant Isolation Bank {label} {run}"},
        expected=201,
        mutation=True,
    )
    assert isinstance(result, Mutation)
    bank_id = result.body.get("id")
    assert isinstance(bank_id, str)
    return bank_id


def create_operator(api: API, issuer_token: str, bank_id: str, run: str, label: str) -> tuple[str, str]:
    username = f"tenant_isolation_{label.lower()}_{run}"
    password = "Functional-Bank-Operator!2026"
    result = api.request(
        "POST",
        "/v1/users",
        token=issuer_token,
        payload={"username": username, "password": password, "role": "bank_operator", "entity_id": bank_id},
        expected=201,
        mutation=True,
    )
    assert isinstance(result, Mutation)
    assert isinstance(result.body.get("id"), str)
    return username, password


def seed_bank_b(api: API, token: str, bank_id: str, run: str) -> tuple[str, str, str, str, str]:
    product = api.request(
        "POST",
        f"/v1/banks/{bank_id}/card-products",
        token=token,
        payload={"product_code": f"tenant-isolation-{run}", "name": "Tenant Isolation Product", "configuration": {}},
        expected=201,
        mutation=True,
    )
    client = api.request(
        "POST",
        f"/v1/banks/{bank_id}/clients",
        token=token,
        payload={"external_client_ref": f"tenant-isolation-client-{run}", "display_name": "Tenant Isolation Client"},
        expected=201,
        mutation=True,
    )
    assert isinstance(product, Mutation) and isinstance(client, Mutation)
    product_id, client_id = product.body["id"], client.body["id"]

    account = api.request(
        "POST",
        f"/v1/banks/{bank_id}/account-references",
        token=token,
        payload={"client_id": client_id, "external_account_ref": f"tenant-isolation-account-{run}"},
        expected=201,
        mutation=True,
    )
    assert isinstance(account, Mutation)
    account_id = account.body["id"]

    issue = api.request(
        "POST",
        f"/v1/banks/{bank_id}/cards",
        token=token,
        payload={"client_id": client_id, "account_reference_id": account_id, "product_id": product_id, "reason": "tenant isolation fixture"},
        expected=201,
        mutation=True,
    )
    assert isinstance(issue, Mutation)
    card_id = issue.body["card"]["id"]

    batch = api.request(
        "POST",
        f"/v1/banks/{bank_id}/card-status-batches",
        token=token,
        payload={"target_status": "suspended", "reason": "tenant isolation fixture", "card_ids": [card_id]},
        expected=201,
        mutation=True,
    )
    assert isinstance(batch, Mutation)
    batch_id = batch.body["id"]
    assert all(isinstance(value, str) for value in (product_id, client_id, account_id, card_id, batch_id))
    return product_id, client_id, account_id, card_id, batch_id


def snapshot_tenant(connection: psycopg.Connection, bank_id: str, card_id: str, batch_id: str) -> TenantSnapshot:
    counts = one(
        connection,
        "SELECT "
        "(SELECT count(*) FROM bank.card_products WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.clients WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.account_references WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.cards WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.card_operations WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.card_status_history WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.card_status_batches WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.card_status_batch_items WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.audit_events WHERE entity_id=%s), "
        "(SELECT count(*) FROM bank.idempotency_records WHERE entity_id=%s)",
        (bank_id,) * 10,
    )
    card_status, card_version = one(
        connection,
        "SELECT status::text, version FROM bank.cards WHERE entity_id=%s AND id=%s",
        (bank_id, card_id),
    )
    (batch_status,) = one(
        connection,
        "SELECT status::text FROM bank.card_status_batches WHERE entity_id=%s AND id=%s",
        (bank_id, batch_id),
    )
    return TenantSnapshot(*counts, card_status, card_version, batch_status)


def assert_denied(
    api: API,
    *,
    name: str,
    method: str,
    path: str,
    token: str,
    payload: dict[str, Any] | None,
    expected_status: int,
    expected_code: str,
) -> None:
    request_id = str(uuid.uuid4())
    headers = {"Authorization": f"Bearer {token}", "X-Request-ID": request_id}
    if method in {"POST", "PATCH"}:
        headers["Idempotency-Key"] = f"functional-isolation-denied-{uuid.uuid4().hex}"
    print_endpoint(method, path)
    response = api.client.request(method, path, headers=headers, json=payload)
    assert response.status_code == expected_status, f"{name}: {safe_response_body(response)}"
    assert response.headers["X-Request-ID"] == request_id, name
    assert response.headers["Content-Type"].startswith("application/problem+json"), name
    assert response.headers["Cache-Control"] == "no-store", name
    body = response.json()
    assert isinstance(body, dict), name
    assert body["status"] == expected_status, name
    assert body["code"] == expected_code, name
    assert body["request_id"] == request_id, name
    assert_no_sensitive_fields(body)


def test_bank_operator_cannot_access_another_bank() -> None:
    api = API(os.environ["FUNCTIONAL_BASE_URL"])
    shard = psycopg.connect(database_url(os.environ["FUNCTIONAL_SHARD_DATABASE"]))
    run = uuid.uuid4().hex[:12]
    try:
        issuer_token = login(api, "issuer_operator", "Test-Issuer-Operator!2026")
        bank_a = create_bank(api, issuer_token, run, "A")
        bank_b = create_bank(api, issuer_token, run, "B")
        username_a, password_a = create_operator(api, issuer_token, bank_a, run, "A")
        username_b, password_b = create_operator(api, issuer_token, bank_b, run, "B")
        token_a = login(api, username_a, password_a)
        token_b = login(api, username_b, password_b)

        product_id, client_id, account_id, card_id, batch_id = seed_bank_b(api, token_b, bank_b, run)
        before = snapshot_tenant(shard, bank_b, card_id, batch_id)
        assert before.card_status == "issued"
        assert before.card_version == 1
        assert before.batch_status == "draft"

        bank_path = f"/v1/banks/{bank_b}"
        cases = [
            ("list card products", "GET", f"{bank_path}/card-products", None, 403, "forbidden"),
            ("get card product", "GET", f"{bank_path}/card-products/{product_id}", None, 403, "forbidden"),
            ("create card product", "POST", f"{bank_path}/card-products", {"product_code": f"denied-{run}", "name": "Denied Product", "configuration": {}}, 403, "forbidden"),
            ("patch card product", "PATCH", f"{bank_path}/card-products/{product_id}", {"name": "Denied Product"}, 403, "forbidden"),
            ("list clients", "GET", f"{bank_path}/clients", None, 403, "forbidden"),
            ("get client", "GET", f"{bank_path}/clients/{client_id}", None, 403, "forbidden"),
            ("create client", "POST", f"{bank_path}/clients", {"external_client_ref": f"denied-client-{run}"}, 403, "forbidden"),
            ("patch client", "PATCH", f"{bank_path}/clients/{client_id}", {"display_name": "Denied Client"}, 403, "forbidden"),
            ("list account references", "GET", f"{bank_path}/account-references", None, 403, "forbidden"),
            ("get account reference", "GET", f"{bank_path}/account-references/{account_id}", None, 403, "forbidden"),
            ("create account reference", "POST", f"{bank_path}/account-references", {"client_id": client_id, "external_account_ref": f"denied-account-{run}"}, 403, "forbidden"),
            ("patch account reference", "PATCH", f"{bank_path}/account-references/{account_id}", {"client_id": client_id}, 403, "forbidden"),
            ("list cards", "GET", f"{bank_path}/cards", None, 403, "forbidden"),
            ("get card", "GET", f"{bank_path}/cards/{card_id}", None, 403, "forbidden"),
            ("list card operations", "GET", f"{bank_path}/cards/{card_id}/operations", None, 403, "forbidden"),
            ("list card history", "GET", f"{bank_path}/cards/{card_id}/history", None, 403, "forbidden"),
            ("issue card", "POST", f"{bank_path}/cards", {"client_id": client_id, "account_reference_id": account_id, "product_id": product_id, "reason": "denied issue"}, 403, "forbidden"),
            *[(f"card {action}", "POST", f"{bank_path}/cards/{card_id}:{action}", {"reason": f"denied {action}"}, 403, "forbidden") for action in ("activate", "suspend", "resume", "close", "replace")],
            ("list card status batches", "GET", f"{bank_path}/card-status-batches", None, 403, "forbidden"),
            ("get card status batch", "GET", f"{bank_path}/card-status-batches/{batch_id}", None, 403, "forbidden"),
            ("list card status batch items", "GET", f"{bank_path}/card-status-batches/{batch_id}/items", None, 403, "forbidden"),
            ("create card status batch", "POST", f"{bank_path}/card-status-batches", {"target_status": "suspended", "reason": "denied batch", "card_ids": [card_id]}, 403, "forbidden"),
            *[(f"card status batch {action}", "POST", f"{bank_path}/card-status-batches/{batch_id}:{action}", {}, 403, "forbidden") for action in ("execute", "cancel", "retry")],
            ("retry expiry item", "POST", f"{bank_path}/card-expiry-runs/{uuid.uuid4()}/{uuid.uuid4()}:retry", {"reason": "denied expiry retry"}, 404, "not_found"),
        ]
        assert len(cases) == 30
        for name, method, path, payload, expected_status, expected_code in cases:
            assert_denied(
                api,
                name=name,
                method=method,
                path=path,
                token=token_a,
                payload=payload,
                expected_status=expected_status,
                expected_code=expected_code,
            )

        assert snapshot_tenant(shard, bank_b, card_id, batch_id) == before
    finally:
        api.close()
        shard.close()
