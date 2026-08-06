from __future__ import annotations

from collections.abc import Sequence
from typing import Protocol

from eventrail_ai.retrieval import RetrievalResult
from eventrail_ai.triage import Citation, TriageDecision, TriageRequest, citations_from_results


class TriageProvider(Protocol):
    def generate(
        self,
        request: TriageRequest,
        retrieved: Sequence[RetrievalResult],
    ) -> TriageDecision:
        ...


class DeterministicTriageProvider:
    def generate(
        self,
        request: TriageRequest,
        retrieved: Sequence[RetrievalResult],
    ) -> TriageDecision:
        category = _classify(request)
        return _decision_for_category(request, category, _citations_for_category(category, retrieved))


def _classify(request: TriageRequest) -> str:
    text = _signal_text(request)
    status = request.http_status
    if (
        "malformed json" in text
        or "unsupported schema version" in text
        or "invalid field type" in text
    ):
        return "schema_error"
    if (
        status == 400
        or "invoice_id" in text
        or "missing required field" in text
        or "validation failure" in text
    ):
        return "destination_validation_error"
    if status == 401 or "credentials" in text or "unauthorized" in text:
        return "authentication_error"
    if status == 403 or "forbidden" in text:
        return "authorization_error"
    if status == 429 or "rate limit" in text or "throttled" in text or "retry-after" in text:
        return "rate_limited"
    if (
        (status is not None and 500 <= status <= 599)
        or "timeout" in text
        or "connection refused" in text
        or "connection reset" in text
        or "destination unavailable" in text
    ):
        return "destination_outage"
    if (
        status == 404
        or "incorrect destination url" in text
        or "route not found" in text
        or "routing configuration" in text
    ):
        return "routing_configuration_error"
    return "unknown"


def _decision_for_category(
    request: TriageRequest,
    category: str,
    citations: list[Citation],
) -> TriageDecision:
    destination = request.destination
    if category == "destination_validation_error":
        return TriageDecision(
            category=category,
            summary=f"The {destination} rejected the delivery because required receipt data is invalid or missing.",
            recommended_actions=[
                "Verify the payment-to-receipt field mapping.",
                "Confirm invoice_id is included before delivery.",
                "Validate the destination schema version.",
            ],
            redrive_recommendation="not_ready",
            citations=citations,
        )
    if category == "authentication_error":
        return TriageDecision(
            category=category,
            summary=f"The {destination} rejected the delivery because authentication failed.",
            recommended_actions=[
                "Verify the configured credential reference with the destination owner.",
                "Confirm the credential is present and not expired.",
                "Use the approved secret-management process for any credential fix.",
            ],
            redrive_recommendation="not_ready",
            citations=citations,
        )
    if category == "authorization_error":
        return TriageDecision(
            category=category,
            summary=f"The {destination} rejected the delivery because authorization failed.",
            recommended_actions=[
                "Confirm the credential has permission for receipt creation.",
                "Verify the source is mapped to an allowed destination route.",
                "Ask the destination owner to confirm scope changes before redrive.",
            ],
            redrive_recommendation="not_ready",
            citations=citations,
        )
    if category == "rate_limited":
        return TriageDecision(
            category=category,
            summary=f"The {destination} is rate limiting receipt delivery.",
            recommended_actions=[
                "Review recent replay or redrive activity.",
                "Coordinate a safe delivery rate with the destination owner.",
                "Wait for the destination quota window before operator redrive.",
            ],
            redrive_recommendation="review_required",
            citations=citations,
        )
    if category == "destination_outage":
        return TriageDecision(
            category=category,
            summary=f"The {destination} appears unavailable or degraded during delivery.",
            recommended_actions=[
                "Check destination health with the service owner.",
                "Confirm failures are transport or 5xx errors before changing payloads.",
                "Review duplicate-safe delivery readiness before operator redrive.",
            ],
            redrive_recommendation="review_required",
            citations=citations,
        )
    if category == "schema_error":
        return TriageDecision(
            category=category,
            summary=f"The {destination} rejected the delivery because the receipt schema is incompatible.",
            recommended_actions=[
                "Compare sanitized field names and types with the destination contract.",
                "Check whether the producer changed schema version or serialization.",
                "Fix the schema mismatch before considering operator redrive.",
            ],
            redrive_recommendation="not_ready",
            citations=citations,
        )
    if category == "routing_configuration_error":
        return TriageDecision(
            category=category,
            summary=f"The delivery route for {destination} appears incorrect or unavailable.",
            recommended_actions=[
                "Verify the destination URL, path, and environment.",
                "Confirm the route accepts receipt creation requests.",
                "Correct the route configuration before operator redrive.",
            ],
            redrive_recommendation="not_ready",
            citations=citations,
        )
    return TriageDecision(
        category="unknown",
        summary="The cause could not be determined confidently from the sanitized failure metadata.",
        recommended_actions=[
            "Review the sanitized delivery metadata with the destination owner.",
            "Check destination health, route, authentication, and schema before redrive.",
        ],
        redrive_recommendation="review_required",
        citations=citations,
    )


def _citations_for_category(
    category: str,
    retrieved: Sequence[RetrievalResult],
) -> list[Citation]:
    preferred_runbook = {
        "destination_validation_error": "receipt-validation-v1",
        "authentication_error": "authentication-v1",
        "authorization_error": "authentication-v1",
        "rate_limited": "rate-limiting-v1",
        "destination_outage": "destination-outage-v1",
        "schema_error": "schema-errors-v1",
        "routing_configuration_error": "routing-configuration-v1",
    }.get(category)
    if preferred_runbook is not None:
        for result in retrieved:
            if result.chunk.runbook_id == preferred_runbook:
                return citations_from_results([result])
    return citations_from_results(retrieved[:1])


def _signal_text(request: TriageRequest) -> str:
    values = [
        request.event_type,
        request.business_event_type or "",
        request.source,
        request.destination,
        request.error,
        request.schema_version or "",
    ]
    return " ".join(values).lower()
