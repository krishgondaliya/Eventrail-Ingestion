from __future__ import annotations

from collections.abc import Sequence
from pathlib import Path

from fastapi.testclient import TestClient

from eventrail_ai.api import create_app
from eventrail_ai.runbooks import RunbookChunk
from eventrail_ai.triage import TriageEngine
from eventrail_ai.triage_provider import DeterministicTriageProvider

RUNBOOKS_DIR = Path(__file__).resolve().parents[1] / "runbooks"


def test_health_endpoint_returns_200() -> None:
    client = TestClient(create_app(make_engine()))

    response = client.get("/health/live")

    assert response.status_code == 200
    assert response.json() == {"status": "ok", "service": "eventrail-ai"}


def test_valid_triage_request_returns_required_schema() -> None:
    client = TestClient(create_app(make_engine()))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    body = response.json()
    assert set(body) == {
        "category",
        "summary",
        "recommended_actions",
        "redrive_recommendation",
        "citations",
        "analysis_mode",
    }
    assert body["category"] == "destination_validation_error"
    assert body["analysis_mode"] == "deterministic_runbook"
    assert body["citations"]


def test_unknown_request_fields_are_rejected() -> None:
    client = TestClient(create_app(make_engine()))
    body = valid_body()
    body["extra"] = "nope"

    response = client.post("/triage", json=body)

    assert response.status_code == 422


def test_missing_required_fields_are_rejected() -> None:
    client = TestClient(create_app(make_engine()))
    body = valid_body()
    del body["event_type"]

    response = client.post("/triage", json=body)

    assert response.status_code == 422


def test_complete_payload_fields_are_rejected() -> None:
    client = TestClient(create_app(make_engine()))
    body = valid_body()
    body["payload"] = {"invoice_id": "INV-2048", "amount": 500}

    response = client.post("/triage", json=body)

    assert response.status_code == 422


def test_error_longer_than_1000_characters_is_rejected() -> None:
    client = TestClient(create_app(make_engine()))
    body = valid_body()
    body["error"] = "x" * 1001

    response = client.post("/triage", json=body)

    assert response.status_code == 422


def test_attempt_count_below_one_is_rejected() -> None:
    client = TestClient(create_app(make_engine()))
    body = valid_body()
    body["attempt_count"] = 0

    response = client.post("/triage", json=body)

    assert response.status_code == 422


def test_api_repeated_requests_are_identical() -> None:
    client = TestClient(create_app(make_engine()))

    first = client.post("/triage", json=valid_body()).json()
    second = client.post("/triage", json=valid_body()).json()

    assert first == second


def test_app_index_is_created_once_not_per_request() -> None:
    calls = 0

    def load_chunks(_: Path) -> Sequence[RunbookChunk]:
        nonlocal calls
        calls += 1
        return (
            RunbookChunk(
                runbook_id="receipt-validation-v1",
                chunk_id="receipt-validation-v1/checks",
                title="Receipt Validation Failures",
                heading="Checks",
                version="1",
                categories=("destination_validation_error",),
                source_path="receipt-validation.md",
                content="invoice_id missing required receipt validation",
            ),
        )

    engine = TriageEngine(
        provider=DeterministicTriageProvider(),
        runbooks_dir=RUNBOOKS_DIR,
        load_chunks=load_chunks,
    )
    client = TestClient(create_app(engine))

    client.post("/triage", json=valid_body())
    client.post("/triage", json=valid_body())

    assert calls == 1


def valid_body() -> dict[str, object]:
    return {
        "event_type": "webhook",
        "business_event_type": "invoice.paid",
        "source": "Payment Service",
        "destination": "Receipt Service",
        "http_status": 400,
        "error": "Required field invoice_id was missing",
        "attempt_count": 1,
        "schema_version": "1",
    }


def make_engine() -> TriageEngine:
    return TriageEngine(provider=DeterministicTriageProvider(), runbooks_dir=RUNBOOKS_DIR)
