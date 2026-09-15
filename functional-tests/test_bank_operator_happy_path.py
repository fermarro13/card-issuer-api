import os
import uuid
from dataclasses import dataclass

import httpx
import psycopg


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
    ) -> Mutation | dict:
        request_id = str(uuid.uuid4())
        headers = {"X-Request-ID": request_id}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        key = ""
        if mutation:
            key = f"functional-{uuid.uuid4().hex}"
            headers["Idempotency-Key"] = key
        response = self.client.request(method, path, headers=headers, json=payload)
        print(f"\n=========\nHTTP {method} {path} -> {response.status_code}\n=========")
        assert response.status_code == expected, response.text
        assert response.headers["X-Request-ID"] == request_id
        assert response.headers["Content-Type"].startswith("application/json")
        body = response.json()
        assert isinstance(body, dict)
        if mutation:
            return Mutation(body, request_id, key)
        return body


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
    compact_statement = " ".join(statement.split())
    print(
        f"\n=========\nDB {connection.info.dbname}: {compact_statement} "
        f"params={parameters!r} -> {row!r}\n========="
    )
    assert row is not None, statement
    return row


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
        issuer_login_response = api.client.post(
            "/v1/auth/login",
            headers={"X-Request-ID": issuer_login_request_id},
            json={"username": "issuer_operator", "password": "Test-Issuer-Operator!2026"},
        )
        print(f"\n=========\nHTTP POST /v1/auth/login -> {issuer_login_response.status_code}\n=========")
        assert issuer_login_response.status_code == 200, issuer_login_response.text
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
        bank_login_response = api.client.post(
            "/v1/auth/login",
            headers={"X-Request-ID": bank_login_request_id},
            json={"username": username, "password": password},
        )
        print(f"\n=========\nHTTP POST /v1/auth/login -> {bank_login_response.status_code}\n=========")
        assert bank_login_response.status_code == 200, bank_login_response.text
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
        original_card_id, issue_operation_id = issue.body["card"]["id"], issue.body["operation"]["id"]
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{original_card_id}", token=bank_token)["status"] == "issued"
        assert one(
            shard,
            "SELECT client_id::text, account_reference_id::text, product_id::text, status, version, credential_reference LIKE %s, created_by::text "
            "FROM bank.cards WHERE entity_id=%s AND id=%s",
            ("local_%", bank_id, original_card_id),
        ) == (client_id, account_id, product_id, "issued", 1, True, bank_user_id)
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
        replacement_id, replace_operation_id = replace.body["card"]["id"], replace.body["operation"]["id"]
        assert api.request("GET", f"/v1/banks/{bank_id}/cards/{replacement_id}", token=bank_token)["status"] == "issued"
        assert one(
            shard,
            "SELECT predecessor_card_id::text, client_id::text, account_reference_id::text, product_id::text, status, version, created_by::text "
            "FROM bank.cards WHERE entity_id=%s AND id=%s",
            (bank_id, replacement_id),
        ) == (original_card_id, client_id, account_id, product_id, "issued", 1, bank_user_id)
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
