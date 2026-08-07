from __future__ import annotations

import asyncio
import json
from collections.abc import Sequence
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from eventrail_ai.api import create_app
from eventrail_ai.runbooks import RunbookChunk
from eventrail_ai.triage import TriageEngine
from eventrail_ai.triage_provider import DeterministicTriageProvider, OpenAIProviderFailure

RUNBOOKS_DIR = Path(__file__).resolve().parents[1] / "runbooks"
SECRET_KEY = "test-secret-value"


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
        "provider",
        "model",
    }
    assert body["category"] == "destination_validation_error"
    assert body["analysis_mode"] == "deterministic_runbook"
    assert body["provider"] == "deterministic"
    assert body["model"] is None
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


def test_deterministic_is_default_and_requires_no_key() -> None:
    client = TestClient(create_app(env={}))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    assert response.json()["analysis_mode"] == "deterministic_runbook"
    assert response.json()["provider"] == "deterministic"


def test_deterministic_does_not_initialize_openai() -> None:
    client = TestClient(create_app(env={"TRIAGE_PROVIDER": "deterministic"}, openai_client=object()))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    assert response.json()["provider"] == "deterministic"


def test_unsupported_provider_is_rejected() -> None:
    with pytest.raises(ValueError, match="TRIAGE_PROVIDER"):
        create_app(env={"TRIAGE_PROVIDER": "mystery"})


def test_openai_without_key_fails_clearly() -> None:
    with pytest.raises(ValueError, match="OPENAI_API_KEY"):
        create_app(env={"TRIAGE_PROVIDER": "openai"})


def test_invalid_timeout_is_rejected_without_exposing_key() -> None:
    with pytest.raises(ValueError) as exc:
        create_app(
            env={
                "TRIAGE_PROVIDER": "openai",
                "OPENAI_API_KEY": SECRET_KEY,
                "OPENAI_TIMEOUT_SECONDS": "0",
            },
            openai_client=FakeOpenAIClient(valid_openai_payload()),
        )

    assert "OPENAI_TIMEOUT_SECONDS" in str(exc.value)
    assert SECRET_KEY not in str(exc.value)


def test_openai_success_returns_grounded_metadata_and_trusted_citations() -> None:
    fake = FakeOpenAIClient(valid_openai_payload())
    client = TestClient(create_app(env=openai_env(), openai_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    body = response.json()
    assert body["analysis_mode"] == "llm_grounded"
    assert body["provider"] == "openai"
    assert body["model"] == "gpt-5-test"
    assert body["citations"][0]["runbook_id"] == "receipt-validation-v1"
    assert body["citations"][0]["title"] == "Receipt Validation Failures"


def test_openai_request_shape_and_prompt_are_sanitized() -> None:
    fake = FakeOpenAIClient(valid_openai_payload())
    client = TestClient(create_app(env=openai_env(), openai_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    request = fake.responses.requests[0]
    assert request["model"] == "gpt-5-test"
    assert request["store"] is False
    assert request["max_output_tokens"] == 700
    assert request["text"]["format"]["type"] == "json_schema"
    assert request["text"]["format"]["strict"] is True
    assert request["text"]["format"]["schema"]["additionalProperties"] is False
    prompt = request["input"]
    assert "SANITIZED FAILURE METADATA" in prompt
    assert "TRUSTED RUNBOOK EXCERPTS" in prompt
    assert prompt.count("Chunk ID:") == 3
    assert "receipt-validation-v1/" in prompt
    for forbidden in ("INV-2048", "$500.00", "USD", "http://", "Idempotency-Key"):
        assert forbidden not in prompt


def valid_openai_payload(
    *,
    category: str = "destination_validation_error",
    recommended_actions: list[str] | None = None,
    citation_chunk_ids: list[str] | None = None,
) -> str:
    return json.dumps(
        {
            "category": category,
            "summary": "The Receipt Service rejected the delivery because invoice_id was missing.",
            "recommended_actions": recommended_actions
            or [
                "Verify the payment-to-receipt field mapping.",
                "Confirm invoice_id is included before delivery.",
            ],
            "redrive_recommendation": "not_ready",
            "citation_chunk_ids": citation_chunk_ids or ["receipt-validation-v1/symptoms"],
        }
    )


@pytest.mark.parametrize(
    "payload",
    [
        pytest.param(OpenAIProviderFailure("network down"), id="api-exception"),
        pytest.param(TimeoutError("timeout"), id="timeout"),
        pytest.param(asyncio.CancelledError("cancelled"), id="cancelled"),
        pytest.param("{not-json", id="invalid-json"),
        pytest.param("", id="empty-output"),
        pytest.param(valid_openai_payload(category="unsupported"), id="unsupported-category"),
        pytest.param(valid_openai_payload(citation_chunk_ids=["not-retrieved"]), id="unknown-citation"),
        pytest.param(
            valid_openai_payload(
                citation_chunk_ids=[
                    "receipt-validation-v1/symptoms",
                    "receipt-validation-v1/symptoms",
                ]
            ),
            id="duplicate-citation",
        ),
        pytest.param(
            valid_openai_payload(recommended_actions=["Automatically redrive the event."]),
            id="forbidden-recommendation",
        ),
    ],
)
def test_openai_provider_failures_fallback_through_api(payload: object) -> None:
    fake = FakeOpenAIClient(payload)
    client = TestClient(create_app(env=openai_env(), openai_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    body = response.json()
    assert body["analysis_mode"] == "deterministic_fallback"
    assert body["provider"] == "deterministic"
    assert body["model"] is None
    assert "network down" not in response.text
    assert "timeout" not in response.text.lower()


@pytest.mark.parametrize(
    "response_status",
    ["refused", "incomplete"],
)
def test_noncompleted_openai_responses_fallback(response_status: str) -> None:
    fake = FakeOpenAIClient(valid_openai_payload(), status=response_status)
    client = TestClient(create_app(env=openai_env(), openai_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    assert response.json()["analysis_mode"] == "deterministic_fallback"


def test_openai_refusal_falls_back() -> None:
    fake = FakeOpenAIClient(valid_openai_payload(), refusal=True)
    client = TestClient(create_app(env=openai_env(), openai_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    assert response.json()["analysis_mode"] == "deterministic_fallback"


def test_ollama_is_accepted_and_requires_no_api_key() -> None:
    fake = FakeOllamaClient(valid_ollama_payload())
    client = TestClient(create_app(env={"TRIAGE_PROVIDER": "ollama"}, ollama_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    body = response.json()
    assert body["analysis_mode"] == "llm_grounded"
    assert body["provider"] == "ollama"
    assert body["model"] == "qwen3:4b"


def test_ollama_does_not_initialize_openai() -> None:
    fake = FakeOllamaClient(valid_ollama_payload())
    client = TestClient(
        create_app(
            env={"TRIAGE_PROVIDER": "ollama"},
            openai_client=object(),
            ollama_client=fake,
        )
    )

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    assert response.json()["provider"] == "ollama"


def test_ollama_defaults_are_used() -> None:
    fake = FakeOllamaClient(valid_ollama_payload())
    client = TestClient(create_app(env={"TRIAGE_PROVIDER": "ollama"}, ollama_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    request = fake.requests[0]
    assert request["url"] == "http://127.0.0.1:11434/api/chat"
    assert request["json"]["model"] == "qwen3:4b"
    assert request["timeout"] == 45.0


def test_ollama_base_url_is_normalized() -> None:
    fake = FakeOllamaClient(valid_ollama_payload())
    client = TestClient(
        create_app(
            env={
                "TRIAGE_PROVIDER": "ollama",
                "OLLAMA_BASE_URL": "http://127.0.0.1:11434///",
            },
            ollama_client=fake,
        )
    )

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    assert fake.requests[0]["url"] == "http://127.0.0.1:11434/api/chat"


@pytest.mark.parametrize("timeout", ["0", "-1", "not-a-number"])
def test_invalid_ollama_timeout_is_rejected_without_exposing_key(timeout: str) -> None:
    with pytest.raises(ValueError) as exc:
        create_app(
            env={
                "TRIAGE_PROVIDER": "ollama",
                "OLLAMA_TIMEOUT_SECONDS": timeout,
                "OPENAI_API_KEY": SECRET_KEY,
            },
            ollama_client=FakeOllamaClient(valid_ollama_payload()),
        )

    assert "OLLAMA_TIMEOUT_SECONDS" in str(exc.value)
    assert SECRET_KEY not in str(exc.value)


def test_empty_ollama_model_is_rejected() -> None:
    with pytest.raises(ValueError, match="OLLAMA_MODEL"):
        create_app(
            env={"TRIAGE_PROVIDER": "ollama", "OLLAMA_MODEL": " "},
            ollama_client=FakeOllamaClient(valid_ollama_payload()),
        )


def test_ollama_request_shape_and_prompt_are_sanitized() -> None:
    fake = FakeOllamaClient(valid_ollama_payload())
    client = TestClient(create_app(env=ollama_env(), ollama_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    request = fake.requests[0]
    assert request["url"] == "http://localhost:11434/api/chat"
    assert request["timeout"] == 12.0
    body = request["json"]
    assert body["model"] == "qwen3-test"
    assert body["stream"] is False
    assert body["think"] is False
    assert body["options"]["temperature"] == 0
    assert body["keep_alive"] == "10m"
    assert body["format"]["additionalProperties"] is False
    assert body["format"]["properties"]["citation_chunk_ids"]["maxItems"] == 3
    assert len(body["messages"]) == 2
    assert body["messages"][0]["role"] == "system"
    assert body["messages"][1]["role"] == "user"
    prompt = body["messages"][1]["content"]
    assert "SANITIZED FAILURE METADATA" in prompt
    assert "TRUSTED RUNBOOK EXCERPTS" in prompt
    assert prompt.count("Chunk ID:") == 3
    assert "receipt-validation-v1/symptoms" in prompt
    assert "Event type: webhook" in prompt
    assert "Business event type: invoice.paid" in prompt
    assert "Source: Payment Service" in prompt
    assert "Destination: Receipt Service" in prompt
    assert "HTTP status: 400" in prompt
    assert "Sanitized error: Required field invoice_id was missing" in prompt
    assert "Attempt count: 1" in prompt
    assert "Schema version: 1" in prompt
    for forbidden in ("INV-2048", "$500.00", "USD", "http://", "Idempotency-Key"):
        assert forbidden not in prompt


def test_ollama_success_returns_grounded_metadata_and_trusted_citations() -> None:
    fake = FakeOllamaClient(valid_ollama_payload())
    client = TestClient(create_app(env=ollama_env(), ollama_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    body = response.json()
    assert body["analysis_mode"] == "llm_grounded"
    assert body["provider"] == "ollama"
    assert body["model"] == "qwen3-test"
    assert body["citations"][0]["runbook_id"] == "receipt-validation-v1"
    assert body["citations"][0]["title"] == "Receipt Validation Failures"


def test_ollama_can_return_context_specific_results_for_different_errors() -> None:
    fake = FakeOllamaClient(
        [
            valid_ollama_payload(
                summary="The Receipt Service rejected the delivery because invoice_id was missing.",
                actions=["Verify the invoice_id mapping before redrive."],
            ),
            valid_ollama_payload(
                category="destination_outage",
                summary="The Receipt Service timed out while EventRail attempted delivery.",
                actions=["Check Receipt Service health before redrive."],
                citation_chunk_ids=["destination-outage-v1/symptoms"],
            ),
        ]
    )
    client = TestClient(create_app(env=ollama_env(), ollama_client=fake))
    outage_body = valid_body()
    outage_body["http_status"] = 503
    outage_body["error"] = "connection timeout destination 503"

    validation_response = client.post("/triage", json=valid_body())
    outage_response = client.post("/triage", json=outage_body)

    assert validation_response.status_code == 200
    assert outage_response.status_code == 200
    validation = validation_response.json()
    outage = outage_response.json()
    assert validation["summary"] != outage["summary"]
    assert validation["recommended_actions"] != outage["recommended_actions"]
    assert outage["category"] == "destination_outage"


@pytest.mark.parametrize(
    "case",
    [
        "connection-error",
        "timeout",
        "cancelled",
        "missing-model",
        "http-500",
        "bad-body",
        "missing-content",
        "empty-content",
        "invalid-json",
        "unsupported-category",
        "unknown-citation",
        "duplicate-citation",
        "forbidden-recommendation",
        "incomplete",
    ],
)
def test_ollama_failures_fallback_through_api(case: str) -> None:
    fake = FakeOllamaClient(ollama_failure_payload(case))
    client = TestClient(create_app(env=ollama_env(), ollama_client=fake))

    response = client.post("/triage", json=valid_body())

    assert response.status_code == 200
    body = response.json()
    assert body["analysis_mode"] == "deterministic_fallback"
    assert body["provider"] == "deterministic"
    assert body["model"] is None
    assert "connection refused" not in response.text
    assert "timed out" not in response.text
    assert "missing model" not in response.text.lower()


def ollama_failure_payload(case: str) -> object:
    if case == "connection-error":
        return ConnectionError("connection refused")
    if case == "timeout":
        return TimeoutError("ollama timed out")
    if case == "cancelled":
        return asyncio.CancelledError("cancelled")
    if case == "missing-model":
        return FakeOllamaResponse(valid_ollama_payload(), status_code=404)
    if case == "http-500":
        return FakeOllamaResponse(valid_ollama_payload(), status_code=500)
    if case == "bad-body":
        return FakeOllamaResponse(valid_ollama_payload(), json_error=True)
    if case == "missing-content":
        return FakeOllamaResponse({"message": {}})
    if case == "empty-content":
        return FakeOllamaResponse({"message": {"content": ""}})
    if case == "invalid-json":
        return FakeOllamaResponse({"message": {"content": "{not-json"}})
    if case == "unsupported-category":
        return FakeOllamaResponse({"message": {"content": valid_ollama_payload(category="bad")}})
    if case == "unknown-citation":
        return FakeOllamaResponse(
            {"message": {"content": valid_ollama_payload(citation_chunk_ids=["not-retrieved"])}}
        )
    if case == "duplicate-citation":
        return FakeOllamaResponse(
            {
                "message": {
                    "content": valid_ollama_payload(
                        citation_chunk_ids=[
                            "receipt-validation-v1/symptoms",
                            "receipt-validation-v1/symptoms",
                        ]
                    )
                }
            }
        )
    if case == "forbidden-recommendation":
        return FakeOllamaResponse(
            {
                "message": {
                    "content": valid_ollama_payload(
                        actions=["Automatically redrive the event."]
                    )
                }
            }
        )
    if case == "incomplete":
        return FakeOllamaResponse({"message": {"content": valid_ollama_payload()}, "done": False})
    raise AssertionError(f"unknown fallback case: {case}")


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


def openai_env() -> dict[str, str]:
    return {
        "TRIAGE_PROVIDER": "openai",
        "OPENAI_API_KEY": SECRET_KEY,
        "OPENAI_MODEL": "gpt-5-test",
        "OPENAI_TIMEOUT_SECONDS": "7",
    }


def ollama_env() -> dict[str, str]:
    return {
        "TRIAGE_PROVIDER": "ollama",
        "OLLAMA_BASE_URL": "http://localhost:11434/",
        "OLLAMA_MODEL": "qwen3-test",
        "OLLAMA_TIMEOUT_SECONDS": "12",
    }


def valid_ollama_payload(
    *,
    category: str = "destination_validation_error",
    summary: str = "The Receipt Service rejected the delivery because invoice_id was missing.",
    actions: list[str] | None = None,
    citation_chunk_ids: list[str] | None = None,
) -> str:
    return json.dumps(
        {
            "category": category,
            "summary": summary,
            "recommended_actions": actions
            or [
                "Verify the payment-to-receipt field mapping.",
                "Confirm invoice_id is included before delivery.",
            ],
            "redrive_recommendation": "not_ready",
            "citation_chunk_ids": citation_chunk_ids or ["receipt-validation-v1/symptoms"],
        }
    )


class FakeOpenAIClient:
    def __init__(self, payload: object, status: str = "completed", refusal: bool = False) -> None:
        self.responses = FakeResponses(payload, status, refusal)


class FakeResponses:
    def __init__(self, payload: object, status: str, refusal: bool) -> None:
        self._payload = payload
        self._status = status
        self._refusal = refusal
        self.requests: list[dict[str, object]] = []

    def create(self, **kwargs: object) -> object:
        self.requests.append(kwargs)
        if isinstance(self._payload, BaseException):
            raise self._payload
        return FakeResponse(output_text=str(self._payload), status=self._status, refusal=self._refusal)


class FakeResponse:
    def __init__(self, output_text: str, status: str, refusal: bool = False) -> None:
        self.output_text = output_text
        self.status = status
        self.error = None
        self.incomplete_details = {"reason": "max_tokens"} if status == "incomplete" else None
        self.refusal = "cannot comply" if refusal else None


class FakeOllamaClient:
    def __init__(self, payload: object) -> None:
        self._payloads: list[object] = payload if isinstance(payload, list) else [payload]
        self._last_payload = self._payloads[-1]
        self.requests: list[dict[str, object]] = []

    def post(self, url: str, *, json: object, timeout: float) -> object:
        self.requests.append({"url": url, "json": json, "timeout": timeout})
        payload = self._payloads.pop(0) if self._payloads else self._last_payload
        self._last_payload = payload
        if isinstance(payload, BaseException):
            raise payload
        if isinstance(payload, FakeOllamaResponse):
            return payload
        return FakeOllamaResponse({"message": {"content": str(payload)}, "done": True})


class FakeOllamaResponse:
    def __init__(
        self,
        body: object,
        *,
        status_code: int = 200,
        json_error: bool = False,
    ) -> None:
        self._body = body
        self.status_code = status_code
        self._json_error = json_error

    def json(self) -> object:
        if self._json_error:
            raise ValueError("invalid json")
        return self._body
