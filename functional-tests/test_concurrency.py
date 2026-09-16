import os
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor

import httpx
import psycopg
import pytest

from test_card_status_batch_activation import (
    API,
    Mutation,
    assert_initial_credentials,
    database_url,
    login,
    one,
    print_endpoint,
    safe_response_body,
    wait_for_completion,
)


BATCH_COUNT = 8
CARDS_PER_BATCH = 4
CONCURRENT_REQUESTS = 16
EXPECTED_EXECUTOR_IDENTITIES = tuple(filter(None, os.environ.get("FUNCTIONAL_EXECUTOR_IDENTITIES", "").split(",")))


pytestmark = pytest.mark.skipif(
    len(EXPECTED_EXECUTOR_IDENTITIES) != 2,
    reason="requires the two executor identities supplied by the Compose smoke test",
)


def concurrent_create(
    base_url: str, token: str, path: str, key: str, payload: dict[str, object]
) -> list[tuple[int, dict[str, object]]]:
    barrier = threading.Barrier(CONCURRENT_REQUESTS)

    def submit() -> tuple[int, dict[str, object]]:
        request_id = str(uuid.uuid4())
        headers = {
            "Authorization": f"Bearer {token}",
            "Idempotency-Key": key,
            "X-Request-ID": request_id,
        }
        with httpx.Client(base_url=base_url, timeout=10.0) as client:
            barrier.wait(timeout=10)
            print_endpoint("POST", path)
            response = client.post(path, headers=headers, json=payload)
        assert response.headers["X-Request-ID"] == request_id
        assert response.headers["Content-Type"].startswith("application/json")
        assert response.status_code in (201, 202), safe_response_body(response)
        body = response.json()
        assert isinstance(body, dict)
        if response.status_code == 202:
            assert body == {"status": "processing"}
        return response.status_code, body

    with ThreadPoolExecutor(max_workers=CONCURRENT_REQUESTS) as workers:
        return list(workers.map(lambda _: submit(), range(CONCURRENT_REQUESTS)))


def replay_create(api: API, token: str, path: str, key: str, payload: dict[str, object]) -> dict[str, object]:
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        request_id = str(uuid.uuid4())
        response = api.client.post(
            path,
            headers={
                "Authorization": f"Bearer {token}",
                "Idempotency-Key": key,
                "X-Request-ID": request_id,
            },
            json=payload,
        )
        assert response.headers["X-Request-ID"] == request_id
        assert response.headers["Content-Type"].startswith("application/json")
        assert response.status_code in (201, 202), safe_response_body(response)
        body = response.json()
        assert isinstance(body, dict)
        if response.status_code == 201:
            return body
        assert body == {"status": "processing"}
        time.sleep(0.1)
    raise AssertionError("idempotent batch create did not complete within 10 seconds")


def test_concurrent_batch_creation_and_executor_claims() -> None:
    api = API(os.environ["FUNCTIONAL_BASE_URL"])
    shard = psycopg.connect(database_url(os.environ["FUNCTIONAL_SHARD_DATABASE"]))
    run = uuid.uuid4().hex[:12]
    try:
        issuer_token, _, _ = login(api, "issuer_operator", "Test-Issuer-Operator!2026")
        bank_create = api.request(
            "POST",
            "/v1/banks",
            token=issuer_token,
            payload={"bank_reference": f"concurrency-{run}", "name": f"Concurrency Bank {run}"},
            expected=201,
            mutation=True,
        )
        assert isinstance(bank_create, Mutation)
        bank_id = bank_create.body["id"]

        username = f"concurrency_{run}"
        password = "Functional-Concurrency-Operator!2026"
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
        bank_token, _, _ = login(api, username, password)

        product = api.request(
            "POST",
            f"/v1/banks/{bank_id}/card-products",
            token=bank_token,
            payload={"product_code": f"concurrency-{run}", "name": "Concurrency Product", "configuration": {}},
            expected=201,
            mutation=True,
        )
        client = api.request(
            "POST",
            f"/v1/banks/{bank_id}/clients",
            token=bank_token,
            payload={"external_client_ref": f"concurrency-client-{run}", "display_name": "Concurrency Client"},
            expected=201,
            mutation=True,
        )
        assert isinstance(product, Mutation) and isinstance(client, Mutation)
        account = api.request(
            "POST",
            f"/v1/banks/{bank_id}/account-references",
            token=bank_token,
            payload={"client_id": client.body["id"], "external_account_ref": f"concurrency-account-{run}"},
            expected=201,
            mutation=True,
        )
        assert isinstance(account, Mutation)

        card_ids: list[str] = []
        for index in range(BATCH_COUNT * CARDS_PER_BATCH):
            issue = api.request(
                "POST",
                f"/v1/banks/{bank_id}/cards",
                token=bank_token,
                payload={
                    "client_id": client.body["id"],
                    "account_reference_id": account.body["id"],
                    "product_id": product.body["id"],
                    "reason": f"concurrency issue {index + 1}",
                },
                expected=201,
                mutation=True,
            )
            assert isinstance(issue, Mutation)
            assert_initial_credentials(issue.body)
            card_ids.append(issue.body["card"]["id"])
        assert len(card_ids) == BATCH_COUNT * CARDS_PER_BATCH
        assert len(set(card_ids)) == len(card_ids)

        batch_path = f"/v1/banks/{bank_id}/card-status-batches"
        create_payload: dict[str, object] = {
            "target_status": "active",
            "reason": "concurrent batch activation",
            "card_ids": card_ids[:CARDS_PER_BATCH],
        }
        create_key = f"concurrent-create-{run}"
        concurrent_results = concurrent_create(os.environ["FUNCTIONAL_BASE_URL"], bank_token, batch_path, create_key, create_payload)
        completed_create = replay_create(api, bank_token, batch_path, create_key, create_payload)
        assert completed_create["status"] == "draft"
        concurrent_batches = [body for status, body in concurrent_results if status == 201]
        assert concurrent_batches
        assert all(body == completed_create for body in concurrent_batches)
        first_batch_id = completed_create["id"]
        assert isinstance(first_batch_id, str)

        scope = f"card-status-batches.create.actor.{bank_user_id}"
        assert one(
            shard,
            "SELECT count(*), count(*) FILTER (WHERE status='succeeded'), count(*) FILTER (WHERE response_status=201) "
            "FROM bank.idempotency_records WHERE entity_id=%s AND operation_scope=%s AND idempotency_key=%s",
            (bank_id, scope, create_key),
        ) == (1, 1, 1)
        assert one(
            shard,
            "SELECT count(*) FROM bank.card_status_batches WHERE entity_id=%s AND id=%s",
            (bank_id, first_batch_id),
        ) == (1,)
        assert one(
            shard,
            "SELECT count(*) FROM bank.card_status_batch_items WHERE entity_id=%s AND batch_id=%s",
            (bank_id, first_batch_id),
        ) == (CARDS_PER_BATCH,)
        assert one(
            shard,
            "SELECT count(*) FROM bank.audit_events WHERE entity_id=%s AND action='card_status_batch.create' "
            "AND resource_type='card_status_batch' AND resource_id=%s",
            (bank_id, first_batch_id),
        ) == (1,)

        batch_ids = [first_batch_id]
        for index in range(1, BATCH_COUNT):
            cards = card_ids[index * CARDS_PER_BATCH : (index + 1) * CARDS_PER_BATCH]
            created = api.request(
                "POST",
                batch_path,
                token=bank_token,
                payload={"target_status": "active", "reason": "replica claim activation", "card_ids": cards},
                expected=201,
                mutation=True,
            )
            assert isinstance(created, Mutation)
            batch_ids.append(created.body["id"])

        for batch_id in batch_ids:
            queued = api.request(
                "POST",
                f"{batch_path}/{batch_id}:execute",
                token=bank_token,
                payload={},
                expected=202,
                mutation=True,
            )
            assert isinstance(queued, Mutation)
            assert queued.body["status"] == "queued"

        for batch_id in batch_ids:
            completed = wait_for_completion(api, bank_token, bank_id, batch_id)
            assert completed["status"] == "succeeded"
            assert completed["applied_count"] == CARDS_PER_BATCH
            assert completed["ignored_count"] == 0

        assert one(
            shard,
            "SELECT count(*), count(*) FILTER (WHERE status='succeeded'), count(*) FILTER (WHERE attempt_count=1), "
            "count(*) FILTER (WHERE lease_owner IS NULL AND lease_expires_at IS NULL) "
            "FROM bank.card_status_batches WHERE entity_id=%s AND id=ANY(%s::uuid[])",
            (bank_id, batch_ids),
        ) == (BATCH_COUNT, BATCH_COUNT, BATCH_COUNT, BATCH_COUNT)
        assert one(
            shard,
            "SELECT count(*), count(*) FILTER (WHERE outcome='applied' AND previous_status='issued' AND failure_code IS NULL) "
            "FROM bank.card_status_batch_items WHERE entity_id=%s AND batch_id=ANY(%s::uuid[])",
            (bank_id, batch_ids),
        ) == (BATCH_COUNT * CARDS_PER_BATCH, BATCH_COUNT * CARDS_PER_BATCH)
        assert one(
            shard,
            "SELECT count(*), count(*) FILTER (WHERE status='active' AND version=2 AND activated_at IS NOT NULL) "
            "FROM bank.cards WHERE entity_id=%s AND id=ANY(%s::uuid[])",
            (bank_id, card_ids),
        ) == (BATCH_COUNT * CARDS_PER_BATCH, BATCH_COUNT * CARDS_PER_BATCH)
        assert one(
            shard,
            "SELECT count(*) FROM bank.card_status_history h JOIN bank.card_status_batch_items i "
            "ON i.entity_id=h.entity_id AND i.operation_id=h.operation_id "
            "WHERE i.entity_id=%s AND i.batch_id=ANY(%s::uuid[])",
            (bank_id, batch_ids),
        ) == (BATCH_COUNT * CARDS_PER_BATCH,)
        assert one(
            shard,
            "SELECT count(*) FROM bank.audit_events WHERE entity_id=%s AND action='batch.apply' AND resource_type='card' "
            "AND resource_id=ANY(%s::uuid[])",
            (bank_id, card_ids),
        ) == (BATCH_COUNT * CARDS_PER_BATCH,)

        operation_rows = shard.execute(
            "SELECT executor_identity, count(*) FROM bank.card_operations o JOIN bank.card_status_batch_items i "
            "ON i.entity_id=o.entity_id AND i.operation_id=o.id "
            "WHERE i.entity_id=%s AND i.batch_id=ANY(%s::uuid[]) GROUP BY executor_identity",
            (bank_id, batch_ids),
        ).fetchall()
        assert sum(row[1] for row in operation_rows) == BATCH_COUNT * CARDS_PER_BATCH
        assert {row[0] for row in operation_rows} == set(EXPECTED_EXECUTOR_IDENTITIES)
        assert all(row[1] > 0 for row in operation_rows)
    finally:
        api.close()
        shard.close()
