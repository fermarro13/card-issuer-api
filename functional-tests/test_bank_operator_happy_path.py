import os
import ssl
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import httpx
import psycopg
import pytest
from cryptography import x509
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID


SENSITIVE_RESPONSE_FIELDS = frozenset({"pan", "cvv", "credentials", "credential_reference"})
AUTHORIZATION_RESPONSE_FIELDS = frozenset({"bank_transaction_reference", "decision_id", "decision"})


@dataclass(frozen=True)
class EphemeralCredentials:
    """Credential material obtained at runtime and never included in assertion output."""

    pan: str
    cvv: str


@dataclass(frozen=True)
class Mutation:
    body: dict
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
        payload: dict | None = None,
        expected: int = 200,
        mutation: bool = False,
        idempotency_key: str | None = None,
    ) -> Mutation | dict:
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
    """Emit a PCI-safe request trace without status, data, or credentials."""
    print(f"HTTP {method} {path}")


def database_url(database: str) -> str:
    return "host={host} port={port} user={user} password={password} dbname={database}".format(
        host=os.environ["FUNCTIONAL_PGHOST"],
        port=os.environ["FUNCTIONAL_PGPORT"],
        user=os.environ["FUNCTIONAL_PGUSER"],
        password=os.environ["FUNCTIONAL_PGPASSWORD"],
        database=database,
    )


def one(connection: psycopg.Connection, statement: str, parameters: tuple) -> tuple:
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
        assert not (set(key.lower() for key in value) & SENSITIVE_RESPONSE_FIELDS), redact(value)
        for item in value.values():
            assert_no_sensitive_fields(item)
    elif isinstance(value, list):
        for item in value:
            assert_no_sensitive_fields(item)


def credentials_from_initial_disclosure(body: dict) -> EphemeralCredentials:
    assert set(body) == {"card", "operation", "credentials"}, redact(body)
    credentials = body.get("credentials")
    assert isinstance(credentials, dict), redact(body)
    assert set(credentials) == {"pan", "cvv"}, redact(body)
    assert_no_sensitive_fields(body["card"])
    assert_no_sensitive_fields(body["operation"])
    pan, cvv = credentials.get("pan"), credentials.get("cvv")
    assert isinstance(pan, str) and 13 <= len(pan) <= 19 and pan.isdecimal(), "invalid transient PAN disclosure"
    assert isinstance(cvv, str) and len(cvv) in (3, 4) and cvv.isdecimal(), "invalid transient CVV disclosure"
    return EphemeralCredentials(pan=pan, cvv=cvv)


def assert_sanitized_issuance_response(body: dict) -> None:
    assert set(body) == {"card", "operation"}, redact(body)
    assert_no_sensitive_fields(body)


def assert_sanitized_replay(initial: dict, replay: dict) -> None:
    assert_sanitized_issuance_response(replay)
    expected = {key: value for key, value in initial.items() if key != "credentials"}
    assert redact(replay) == redact(expected)


def assert_central_idempotency(connection: psycopg.Connection, scope: str, key: str, result: str) -> None:
    assert one(
        connection,
        "SELECT response_status, result_reference::text FROM control.command_idempotency_records WHERE operation_scope=%s AND idempotency_key=%s",
        (scope, key),
    ) == (201, result)


def assert_shard_idempotency(connection: psycopg.Connection, bank: str, scope: str, key: str, result: str) -> None:
    assert one(
        connection,
        "SELECT status, result_reference::text FROM bank.idempotency_records WHERE entity_id=%s AND operation_scope=%s AND idempotency_key=%s",
        (bank, scope, key),
    ) == ("succeeded", result)


def assert_audit(
    connection: psycopg.Connection,
    bank: str,
    request_id: str,
    action: str,
    resource_type: str,
    resource_id: str,
    actor_id: str,
) -> None:
    assert one(
        connection,
        "SELECT actor_user_id::text, actor_role, actor_entity_id::text, action, resource_type, resource_id::text, outcome "
        "FROM bank.audit_events WHERE entity_id=%s AND request_id=%s",
        (bank, request_id),
    ) == (actor_id, "bank_operator", bank, action, resource_type, resource_id, "succeeded")


def assert_login(connection: psycopg.Connection, request_id: str, user_id: str, entity_id: str | None) -> None:
    assert one(
        connection,
        "SELECT event_type, outcome, actor_user_id::text, entity_id::text, session_id IS NOT NULL, "
        "(SELECT revoked_at IS NULL FROM control.auth_sessions WHERE id=a.session_id) "
        "FROM control.authentication_audit_events a WHERE request_id=%s",
        (request_id,),
    ) == ("login", "succeeded", user_id, entity_id, True, True)


def login(api: API, username: str, password: str) -> tuple[str, Mutation]:
    result = api.request(
        "POST",
        "/v1/auth/login",
        payload={"username": username, "password": password},
        mutation=False,
    )
    assert isinstance(result, dict)
    token = result.get("access_token")
    assert isinstance(token, str) and token
    return token, Mutation(result, "", "")


def test_bank_operator_happy_path() -> None:
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

        issuer_login_request_id = str(uuid.uuid4())
        print_endpoint("POST", "/v1/auth/login")
        issuer_login_response = api.client.post(
            "/v1/auth/login",
            headers={"X-Request-ID": issuer_login_request_id},
            json={"username": "issuer_operator", "password": "Test-Issuer-Operator!2026"},
        )
        assert issuer_login_response.status_code == 200, safe_response_body(issuer_login_response)
        issuer = issuer_login_response.json()
        issuer_token = issuer["access_token"]
        assert api.request("GET", "/v1/me", token=issuer_token) == issuer["user"]
        assert_login(control, issuer_login_request_id, issuer_id, None)

        bank_create = api.request(
            "POST",
            "/v1/banks",
            token=issuer_token,
            payload={"bank_reference": f"functional-{run}", "name": f"Functional Bank {run}"},
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
        assert_central_idempotency(control, f"banks.create.actor.{issuer_id}", bank_create.idempotency_key, bank_id)
        assert one(
            shard,
            "SELECT bank_reference, name, status, created_by::text FROM bank.entities WHERE id=%s",
            (bank_id,),
        ) == (f"functional-{run}", f"Functional Bank {run}", "active", issuer_id)
        assert one(
            shard,
            "SELECT actor_user_id::text, actor_role, action, resource_type, resource_id::text, outcome "
            "FROM bank.audit_events WHERE entity_id=%s AND request_id=%s",
            (bank_id, bank_create.request_id),
        ) == (issuer_id, "issuer_operator", "bank_provision", "bank", bank_id, "succeeded")

        username = f"functional_{run}"
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
            "SELECT normalized_username, password_hash LIKE %s, role, entity_id::text, status, created_by::text "
            "FROM control.users WHERE id=%s",
            ("$argon2id$%", bank_user_id),
        ) == (username, True, "bank_operator", bank_id, "enabled", issuer_id)
        assert_central_idempotency(control, f"users.create.actor.{issuer_id}", user_create.idempotency_key, bank_user_id)
        assert one(
            control,
            "SELECT event_type, outcome, actor_user_id::text, subject_user_id::text, entity_id::text "
            "FROM control.authentication_audit_events WHERE request_id=%s",
            (user_create.request_id,),
        ) == ("user_provision", "succeeded", issuer_id, bank_user_id, bank_id)

        bank_login_request_id = str(uuid.uuid4())
        print_endpoint("POST", "/v1/auth/login")
        bank_login_response = api.client.post(
            "/v1/auth/login",
            headers={"X-Request-ID": bank_login_request_id},
            json={"username": username, "password": password},
        )
        assert bank_login_response.status_code == 200, safe_response_body(bank_login_response)
        bank_login = bank_login_response.json()
        bank_token = bank_login["access_token"]
        assert api.request("GET", "/v1/me", token=bank_token) == bank_login["user"]
        assert_login(control, bank_login_request_id, bank_user_id, bank_id)

        product_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/card-products",
            token=bank_token,
            payload={"product_code": f"functional-{run}", "name": "Functional Product", "configuration": {}},
            expected=201,
            mutation=True,
        )
        assert isinstance(product_create, Mutation)
        product_id = product_create.body["id"]
        assert api.request("GET", f"/v1/banks/{bank_id}/card-products/{product_id}", token=bank_token)["id"] == product_id
        assert one(
            shard,
            "SELECT product_code, name, status, configuration, created_by::text FROM bank.card_products WHERE entity_id=%s AND id=%s",
            (bank_id, product_id),
        ) == (f"functional-{run}", "Functional Product", "active", {}, bank_user_id)
        assert_shard_idempotency(shard, bank_id, f"card-products.create.actor.{bank_user_id}", product_create.idempotency_key, product_id)
        assert_audit(shard, bank_id, product_create.request_id, "card-products.create", "card-products", product_id, bank_user_id)

        client_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/clients",
            token=bank_token,
            payload={"external_client_ref": f"functional-client-{run}", "display_name": "Functional Client"},
            expected=201,
            mutation=True,
        )
        assert isinstance(client_create, Mutation)
        client_id = client_create.body["id"]
        assert api.request("GET", f"/v1/banks/{bank_id}/clients/{client_id}", token=bank_token)["id"] == client_id
        assert one(
            shard,
            "SELECT external_client_ref, display_name, created_by::text FROM bank.clients WHERE entity_id=%s AND id=%s",
            (bank_id, client_id),
        ) == (f"functional-client-{run}", "Functional Client", bank_user_id)
        assert_shard_idempotency(shard, bank_id, f"clients.create.actor.{bank_user_id}", client_create.idempotency_key, client_id)
        assert_audit(shard, bank_id, client_create.request_id, "clients.create", "clients", client_id, bank_user_id)

        account_create = api.request(
            "POST",
            f"/v1/banks/{bank_id}/account-references",
            token=bank_token,
            payload={"client_id": client_id, "external_account_ref": f"functional-account-{run}"},
            expected=201,
            mutation=True,
        )
        assert isinstance(account_create, Mutation)
        account_id = account_create.body["id"]
        assert api.request("GET", f"/v1/banks/{bank_id}/account-references/{account_id}", token=bank_token)["id"] == account_id
        assert one(
            shard,
            "SELECT client_id::text, external_account_ref, created_by::text FROM bank.account_references WHERE entity_id=%s AND id=%s",
            (bank_id, account_id),
        ) == (client_id, f"functional-account-{run}", bank_user_id)
        assert_shard_idempotency(shard, bank_id, f"account-references.create.actor.{bank_user_id}", account_create.idempotency_key, account_id)
        assert_audit(shard, bank_id, account_create.request_id, "account-references.create", "account-references", account_id, bank_user_id)

        issue = api.request(
            "POST",
            f"/v1/banks/{bank_id}/cards",
            token=bank_token,
            payload={"client_id": client_id, "account_reference_id": account_id, "product_id": product_id, "reason": "functional issue"},
            expected=201,
            mutation=True,
        )
        assert isinstance(issue, Mutation)
        credentials_from_initial_disclosure(issue.body)
        original_card_id, issue_operation_id = issue.body["card"]["id"], issue.body["operation"]["id"]
        issue_replay = api.request(
            "POST",
            f"/v1/banks/{bank_id}/cards",
            token=bank_token,
            payload={"client_id": client_id, "account_reference_id": account_id, "product_id": product_id, "reason": "functional issue"},
            expected=201,
            mutation=True,
            idempotency_key=issue.idempotency_key,
        )
        assert isinstance(issue_replay, Mutation)
        assert_sanitized_replay(issue.body, issue_replay.body)
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{original_card_id}", token=bank_token)["status"] == "issued"
        assert one(
            shard,
            "SELECT client_id::text, account_reference_id::text, product_id::text, status, version, masked_pan ~ %s, created_by::text, "
            "NOT EXISTS (SELECT FROM information_schema.columns WHERE table_schema='bank' AND table_name='cards' AND column_name='credential_reference') "
            "FROM bank.cards WHERE entity_id=%s AND id=%s",
            (r"^[0-9]{6}\*{6}[0-9]{4}$", bank_id, original_card_id),
        ) == (client_id, account_id, product_id, "issued", 1, True, bank_user_id, True)
        assert one(
            shard,
            "SELECT response_body::text !~ '\"(credentials|credential_reference|pan|cvv)\"' FROM bank.idempotency_records "
            "WHERE entity_id=%s AND operation_scope=%s AND idempotency_key=%s",
            (bank_id, f"cards.issue.actor.{bank_user_id}", issue.idempotency_key),
        ) == (True,)
        assert_card_evidence(shard, bank_id, original_card_id, issue_operation_id, "issue", "pending", "issued", issue.request_id, issue.idempotency_key, f"cards.issue.actor.{bank_user_id}", bank_user_id)
        assert_card_api_evidence(api, bank_token, bank_id, original_card_id, issue_operation_id, "issued")

        activate = card_command(api, bank_token, bank_id, original_card_id, "activate", "functional activate")
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{original_card_id}", token=bank_token)["status"] == "active"
        assert_card_state(shard, bank_id, original_card_id, "active", 2, "activated_at")
        assert_card_evidence(shard, bank_id, original_card_id, activate.body["operation"]["id"], "activate", "issued", "active", activate.request_id, activate.idempotency_key, f"cards.activate.{original_card_id}.actor.{bank_user_id}", bank_user_id)
        assert_card_api_evidence(api, bank_token, bank_id, original_card_id, activate.body["operation"]["id"], "active")

        suspend = card_command(api, bank_token, bank_id, original_card_id, "suspend", "functional suspend")
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{original_card_id}", token=bank_token)["status"] == "suspended"
        assert_card_state(shard, bank_id, original_card_id, "suspended", 3, "suspended_at")
        assert_card_evidence(shard, bank_id, original_card_id, suspend.body["operation"]["id"], "suspend", "active", "suspended", suspend.request_id, suspend.idempotency_key, f"cards.suspend.{original_card_id}.actor.{bank_user_id}", bank_user_id)
        assert_card_api_evidence(api, bank_token, bank_id, original_card_id, suspend.body["operation"]["id"], "suspended")

        resume = card_command(api, bank_token, bank_id, original_card_id, "resume", "functional resume")
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{original_card_id}", token=bank_token)["status"] == "active"
        assert_card_state(shard, bank_id, original_card_id, "active", 4, None)
        assert_card_evidence(shard, bank_id, original_card_id, resume.body["operation"]["id"], "resume", "suspended", "active", resume.request_id, resume.idempotency_key, f"cards.resume.{original_card_id}.actor.{bank_user_id}", bank_user_id)
        assert_card_api_evidence(api, bank_token, bank_id, original_card_id, resume.body["operation"]["id"], "active")

        replace = card_command(api, bank_token, bank_id, original_card_id, "replace", "functional replace", expected=201)
        credentials_from_initial_disclosure(replace.body)
        replacement_id, replace_operation_id = replace.body["card"]["id"], replace.body["operation"]["id"]
        replace_replay = api.request(
            "POST",
            f"/v1/banks/{bank_id}/cards/{original_card_id}:replace",
            token=bank_token,
            payload={"reason": "functional replace"},
            expected=201,
            mutation=True,
            idempotency_key=replace.idempotency_key,
        )
        assert isinstance(replace_replay, Mutation)
        assert_sanitized_replay(replace.body, replace_replay.body)
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{replacement_id}", token=bank_token)["status"] == "issued"
        assert one(
            shard,
            "SELECT predecessor_card_id::text, client_id::text, account_reference_id::text, product_id::text, status, version, created_by::text "
            "FROM bank.cards WHERE entity_id=%s AND id=%s",
            (bank_id, replacement_id),
        ) == (original_card_id, client_id, account_id, product_id, "issued", 1, bank_user_id)
        assert one(
            shard,
            "SELECT response_body::text !~ '\"(credentials|credential_reference|pan|cvv)\"' FROM bank.idempotency_records "
            "WHERE entity_id=%s AND operation_scope=%s AND idempotency_key=%s",
            (bank_id, f"cards.replace.{original_card_id}.actor.{bank_user_id}", replace.idempotency_key),
        ) == (True,)
        assert_card_evidence(shard, bank_id, replacement_id, replace_operation_id, "replace", "pending", "issued", replace.request_id, replace.idempotency_key, f"cards.replace.{original_card_id}.actor.{bank_user_id}", bank_user_id)
        assert_card_api_evidence(api, bank_token, bank_id, replacement_id, replace_operation_id, "issued")

        replacement_activate = card_command(api, bank_token, bank_id, replacement_id, "activate", "functional replacement activate")
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{replacement_id}", token=bank_token)["status"] == "active"
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{original_card_id}", token=bank_token)["status"] == "closed"
        assert_card_state(shard, bank_id, replacement_id, "active", 2, "activated_at")
        assert_card_state(shard, bank_id, original_card_id, "closed", 5, "closed_at")
        assert_card_evidence(shard, bank_id, replacement_id, replacement_activate.body["operation"]["id"], "activate", "issued", "active", replacement_activate.request_id, replacement_activate.idempotency_key, f"cards.activate.{replacement_id}.actor.{bank_user_id}", bank_user_id)
        assert_card_api_evidence(api, bank_token, bank_id, replacement_id, replacement_activate.body["operation"]["id"], "active")
        assert one(
            shard,
            "SELECT count(*) FROM bank.card_operations WHERE entity_id=%s AND card_id=%s AND action='close' AND status='succeeded'",
            (bank_id, original_card_id),
        ) == (1,)
        assert one(
            shard,
            "SELECT count(*) FROM bank.card_status_history WHERE entity_id=%s AND card_id=%s AND new_status='closed'",
            (bank_id, original_card_id),
        ) == (1,)
    finally:
        api.close()
        control.close()
        shard.close()


def card_command(api: API, token: str, bank_id: str, card_id: str, action: str, reason: str, expected: int = 200) -> Mutation:
    result = api.request(
        "POST",
        f"/v1/banks/{bank_id}/cards/{card_id}:{action}",
        token=token,
        payload={"reason": reason},
        expected=expected,
        mutation=True,
    )
    assert isinstance(result, Mutation)
    return result


def assert_card_state(connection: psycopg.Connection, bank: str, card_id: str, status: str, version: int, timestamp: str | None) -> None:
    if timestamp:
        assert one(
            connection,
            f"SELECT status, version, {timestamp} IS NOT NULL FROM bank.cards WHERE entity_id=%s AND id=%s",
            (bank, card_id),
        ) == (status, version, True)
        return
    assert one(connection, "SELECT status, version FROM bank.cards WHERE entity_id=%s AND id=%s", (bank, card_id)) == (status, version)


def assert_card_evidence(
    connection: psycopg.Connection,
    bank: str,
    card_id: str,
    operation_id: str,
    action: str,
    previous: str,
    current: str,
    request_id: str,
    idempotency_key: str,
    idempotency_scope: str,
    actor_id: str,
) -> None:
    assert one(
        connection,
        "SELECT card_id::text, action, status, actor_user_id::text, actor_role, actor_entity_id::text, request_id::text "
        "FROM bank.card_operations WHERE entity_id=%s AND id=%s",
        (bank, operation_id),
    ) == (card_id, action, "succeeded", actor_id, "bank_operator", bank, request_id)
    assert one(
        connection,
        "SELECT previous_status::text, new_status::text, actor_user_id::text, actor_role, actor_entity_id::text "
        "FROM bank.card_status_history WHERE entity_id=%s AND operation_id=%s",
        (bank, operation_id),
    ) == (previous, current, actor_id, "bank_operator", bank)
    assert_shard_idempotency(connection, bank, idempotency_scope, idempotency_key, operation_id)
    assert_audit(connection, bank, request_id, f"card.{action}", "card", card_id, actor_id)


def assert_card_api_evidence(api: API, token: str, bank_id: str, card_id: str, operation_id: str, status: str) -> None:
    operations = api.request("GET", f"/v1/banks/{bank_id}/cards/{card_id}/operations", token=token)["data"]
    history = api.request("GET", f"/v1/banks/{bank_id}/cards/{card_id}/history", token=token)["data"]
    assert any(operation["id"] == operation_id and operation["status"] == "succeeded" for operation in operations)
    assert any(entry["new_status"] == status for entry in history)


@dataclass(frozen=True)
class AuthorizationProvisioning:
    url: str
    bank_id: str
    ca_file: Path
    mapped_certificate_file: Path
    mapped_key_file: Path
    client_ca_certificate_file: Path
    client_ca_key_file: Path


def authorization_provisioning() -> AuthorizationProvisioning:
    names = {
        "url": "FUNCTIONAL_AUTHORIZATION_URL",
        "bank_id": "FUNCTIONAL_AUTHORIZATION_BANK_ID",
        "ca_file": "FUNCTIONAL_AUTHORIZATION_CA_FILE",
        "mapped_certificate_file": "FUNCTIONAL_AUTHORIZATION_MAPPED_CERT_FILE",
        "mapped_key_file": "FUNCTIONAL_AUTHORIZATION_MAPPED_KEY_FILE",
        "client_ca_certificate_file": "FUNCTIONAL_AUTHORIZATION_CLIENT_CA_CERT_FILE",
        "client_ca_key_file": "FUNCTIONAL_AUTHORIZATION_CLIENT_CA_KEY_FILE",
    }
    values = {field: os.environ.get(environment) for field, environment in names.items()}
    missing = [environment for field, environment in names.items() if not values[field]]
    if missing:
        pytest.fail("mTLS/vault provisioning unavailable: missing " + ", ".join(missing))
    provisioning = AuthorizationProvisioning(
        url=str(values["url"]),
        bank_id=str(values["bank_id"]),
        ca_file=Path(str(values["ca_file"])),
        mapped_certificate_file=Path(str(values["mapped_certificate_file"])),
        mapped_key_file=Path(str(values["mapped_key_file"])),
        client_ca_certificate_file=Path(str(values["client_ca_certificate_file"])),
        client_ca_key_file=Path(str(values["client_ca_key_file"])),
    )
    unavailable = [str(path) for path in (
        provisioning.ca_file,
        provisioning.mapped_certificate_file,
        provisioning.mapped_key_file,
        provisioning.client_ca_certificate_file,
        provisioning.client_ca_key_file,
    ) if not path.is_file()]
    if unavailable:
        pytest.fail("mTLS/vault provisioning unavailable: certificate material is not mounted")
    return provisioning


def tls_client(url: str, ca_file: Path, certificate_file: Path, key_file: Path) -> httpx.Client:
    context = ssl.create_default_context(cafile=str(ca_file))
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.load_cert_chain(str(certificate_file), str(key_file))
    return httpx.Client(base_url=url, verify=context, timeout=10.0)


def issue_active_card(api: API, token: str, bank_id: str, run: str) -> tuple[str, EphemeralCredentials]:
    product = api.request(
        "POST", f"/v1/banks/{bank_id}/card-products", token=token,
        payload={"product_code": f"authorization-{run}", "name": "Authorization Product", "configuration": {}},
        expected=201, mutation=True,
    )
    client = api.request(
        "POST", f"/v1/banks/{bank_id}/clients", token=token,
        payload={"external_client_ref": f"authorization-client-{run}", "display_name": "Authorization Client"},
        expected=201, mutation=True,
    )
    assert isinstance(product, Mutation) and isinstance(client, Mutation)
    account = api.request(
        "POST", f"/v1/banks/{bank_id}/account-references", token=token,
        payload={"client_id": client.body["id"], "external_account_ref": f"authorization-account-{run}"},
        expected=201, mutation=True,
    )
    assert isinstance(account, Mutation)
    issue = api.request(
        "POST", f"/v1/banks/{bank_id}/cards", token=token,
        payload={"client_id": client.body["id"], "account_reference_id": account.body["id"], "product_id": product.body["id"], "reason": "authorization test issue"},
        expected=201, mutation=True,
    )
    assert isinstance(issue, Mutation)
    credentials = credentials_from_initial_disclosure(issue.body)
    card_id = issue.body["card"]["id"]
    activate = card_command(api, token, bank_id, card_id, "activate", "authorization test activate")
    assert activate.body["card"]["status"] == "active"
    return card_id, credentials


def authorization_request(client: httpx.Client, reference: str, credentials: EphemeralCredentials) -> dict:
    print_endpoint("POST", "/v1/authorization-verifications")
    response = client.post(
        "/v1/authorization-verifications",
        json={"bank_transaction_reference": reference, "pan": credentials.pan, "cvv": credentials.cvv},
    )
    assert response.status_code == 200, safe_response_body(response)
    body = response.json()
    assert isinstance(body, dict)
    assert set(body) == AUTHORIZATION_RESPONSE_FIELDS, redact(body)
    assert body["bank_transaction_reference"] == reference
    assert body["decision"] in {"approved", "declined"}
    return body


def invalid_credentials(credentials: EphemeralCredentials) -> EphemeralCredentials:
    replacement = "0" if credentials.pan[-1] != "0" else "1"
    return EphemeralCredentials(pan=credentials.pan[:-1] + replacement, cvv=credentials.cvv)


def generate_unmapped_client_certificate(provisioning: AuthorizationProvisioning, directory: Path) -> tuple[Path, Path]:
    ca_certificate = x509.load_pem_x509_certificate(provisioning.client_ca_certificate_file.read_bytes())
    ca_key = serialization.load_pem_private_key(provisioning.client_ca_key_file.read_bytes(), password=None)
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    now = datetime.now(UTC)
    certificate = (
        x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "functional-unmapped-client")]))
        .issuer_name(ca_certificate.subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=1))
        .not_valid_after(now + timedelta(minutes=10))
        .add_extension(x509.BasicConstraints(ca=False, path_length=None), critical=True)
        .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.CLIENT_AUTH]), critical=False)
        .sign(ca_key, None)
    )
    certificate_file, key_file = directory / "unmapped-client.pem", directory / "unmapped-client-key.pem"
    certificate_file.write_bytes(certificate.public_bytes(serialization.Encoding.PEM))
    key_file.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    return certificate_file, key_file


def assert_safe_authorization_rows(connection: psycopg.Connection, bank_id: str, reference: str, expected_card_id: str | None) -> None:
    columns = connection.execute(
        "SELECT column_name FROM information_schema.columns WHERE table_schema='bank' AND table_name='authorization_verifications' ORDER BY ordinal_position"
    ).fetchall()
    assert {column[0] for column in columns} == {
        "entity_id", "id", "decision_id", "bank_transaction_reference", "card_id", "decision", "decision_code", "decided_at", "created_at"
    }
    row = one(
        connection,
        "SELECT bank_transaction_reference, decision_id::text, card_id::text, decision, decision_code, decided_at IS NOT NULL, created_at IS NOT NULL "
        "FROM bank.authorization_verifications WHERE entity_id=%s AND bank_transaction_reference=%s",
        (bank_id, reference),
    )
    assert row[0] == reference
    assert isinstance(row[1], str) and row[1]
    assert row[2] == expected_card_id
    assert row[3] in {"approved", "declined"}
    assert row[4] in {"approved", "credential_invalid", "card_not_found", "card_ineligible"}
    assert row[5:] == (True, True)


def test_authorization_vault_mtls_boundary(tmp_path: Path) -> None:
    provisioning = authorization_provisioning()
    api = API(os.environ["FUNCTIONAL_BASE_URL"])
    shard = psycopg.connect(database_url(os.environ["FUNCTIONAL_SHARD_DATABASE"]))
    mapped_client = tls_client(provisioning.url, provisioning.ca_file, provisioning.mapped_certificate_file, provisioning.mapped_key_file)
    run = uuid.uuid4().hex[:12]
    try:
        bank_token, _ = login(api, "bank_operator", "Test-Bank-Operator!2026")
        active_card_id, active_credentials = issue_active_card(api, bank_token, provisioning.bank_id, f"{run}-active")

        approved_reference = f"authorization-{run}-approved"
        approved = authorization_request(mapped_client, approved_reference, active_credentials)
        assert approved["decision"] == "approved"
        assert_safe_authorization_rows(shard, provisioning.bank_id, approved_reference, active_card_id)

        replay = authorization_request(mapped_client, approved_reference, invalid_credentials(active_credentials))
        assert replay == approved
        assert one(
            shard,
            "SELECT count(*) FROM bank.authorization_verifications WHERE entity_id=%s AND bank_transaction_reference=%s",
            (provisioning.bank_id, approved_reference),
        ) == (1,)

        invalid_reference = f"authorization-{run}-invalid"
        assert authorization_request(mapped_client, invalid_reference, invalid_credentials(active_credentials))["decision"] == "declined"
        assert_safe_authorization_rows(shard, provisioning.bank_id, invalid_reference, None)

        suspended_card_id, suspended_credentials = issue_active_card(api, bank_token, provisioning.bank_id, f"{run}-suspended")
        card_command(api, bank_token, provisioning.bank_id, suspended_card_id, "suspend", "authorization test suspend")
        suspended_reference = f"authorization-{run}-suspended"
        assert authorization_request(mapped_client, suspended_reference, suspended_credentials)["decision"] == "declined"
        assert_safe_authorization_rows(shard, provisioning.bank_id, suspended_reference, suspended_card_id)

        closed_card_id, closed_credentials = issue_active_card(api, bank_token, provisioning.bank_id, f"{run}-closed")
        card_command(api, bank_token, provisioning.bank_id, closed_card_id, "close", "authorization test close")
        closed_reference = f"authorization-{run}-closed"
        assert authorization_request(mapped_client, closed_reference, closed_credentials)["decision"] == "declined"
        assert_safe_authorization_rows(shard, provisioning.bank_id, closed_reference, closed_card_id)

        expired_card_id, expired_credentials = issue_active_card(api, bank_token, provisioning.bank_id, f"{run}-expired")
        shard.execute("UPDATE bank.cards SET expires_at=clock_timestamp()-interval '1 second' WHERE entity_id=%s AND id=%s", (provisioning.bank_id, expired_card_id))
        shard.commit()
        expired_reference = f"authorization-{run}-expired"
        assert authorization_request(mapped_client, expired_reference, expired_credentials)["decision"] == "declined"
        assert_safe_authorization_rows(shard, provisioning.bank_id, expired_reference, expired_card_id)

        issuer_token, _ = login(api, "issuer_operator", "Test-Issuer-Operator!2026")
        other_bank = api.request(
            "POST", "/v1/banks", token=issuer_token,
            payload={"bank_reference": f"authorization-other-{run}", "name": "Authorization Other Bank"}, expected=201, mutation=True,
        )
        assert isinstance(other_bank, Mutation)
        other_user = api.request(
            "POST", "/v1/users", token=issuer_token,
            payload={"username": f"authorization_{run}", "password": "Functional-Bank-Operator!2026", "role": "bank_operator", "entity_id": other_bank.body["id"]}, expected=201, mutation=True,
        )
        assert isinstance(other_user, Mutation)
        other_token, _ = login(api, f"authorization_{run}", "Functional-Bank-Operator!2026")
        _, cross_bank_credentials = issue_active_card(api, other_token, other_bank.body["id"], f"{run}-cross-bank")
        cross_reference = f"authorization-{run}-cross-bank"
        assert authorization_request(mapped_client, cross_reference, cross_bank_credentials)["decision"] == "declined"
        assert_safe_authorization_rows(shard, provisioning.bank_id, cross_reference, None)

        no_certificate_context = ssl.create_default_context(cafile=str(provisioning.ca_file))
        no_certificate_context.minimum_version = ssl.TLSVersion.TLSv1_3
        with httpx.Client(base_url=provisioning.url, verify=no_certificate_context, timeout=10.0) as no_certificate_client:
            print_endpoint("POST", "/v1/authorization-verifications")
            try:
                response = no_certificate_client.post(
                    "/v1/authorization-verifications",
                    json={"bank_transaction_reference": f"authorization-{run}-no-client-cert", "pan": active_credentials.pan, "cvv": active_credentials.cvv},
                )
            except httpx.TransportError:
                pass
            else:
                assert response.status_code == 401, safe_response_body(response)

        unmapped_certificate, unmapped_key = generate_unmapped_client_certificate(provisioning, tmp_path)
        with tls_client(provisioning.url, provisioning.ca_file, unmapped_certificate, unmapped_key) as unmapped_client:
            print_endpoint("POST", "/v1/authorization-verifications")
            response = unmapped_client.post(
                "/v1/authorization-verifications",
                json={"bank_transaction_reference": f"authorization-{run}-unmapped", "pan": active_credentials.pan, "cvv": active_credentials.cvv},
            )
        assert response.status_code == 401, safe_response_body(response)

        print_endpoint("POST", "/v1/authorization-verifications")
        response = mapped_client.post(
            "/v1/authorization-verifications",
            json={"bank_transaction_reference": f"authorization-{run}-body-bank", "pan": active_credentials.pan, "cvv": active_credentials.cvv, "bank_id": other_bank.body["id"]},
        )
        assert response.status_code == 400, safe_response_body(response)
        assert one(
            shard,
            "SELECT count(*) FROM bank.authorization_verifications WHERE entity_id=%s AND bank_transaction_reference=%s",
            (other_bank.body["id"], f"authorization-{run}-body-bank"),
        ) == (0,)
    finally:
        mapped_client.close()
        api.close()
        shard.close()
