from __future__ import annotations

import logging
from collections.abc import Callable, Sequence
from pathlib import Path
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator

from eventrail_ai.embeddings import DeterministicHashEmbeddingProvider, EmbeddingProvider
from eventrail_ai.retrieval import IndexedChunk, RetrievalResult, build_index, retrieve
from eventrail_ai.runbooks import RunbookChunk, load_and_chunk_runbooks

RedriveRecommendation = Literal["not_ready", "review_required"]
AnalysisMode = Literal["deterministic_runbook", "llm_grounded", "deterministic_fallback"]
ProviderName = Literal["deterministic", "openai", "ollama"]
TriageCategory = Literal[
    "destination_validation_error",
    "authentication_error",
    "authorization_error",
    "rate_limited",
    "destination_outage",
    "schema_error",
    "routing_configuration_error",
    "unknown",
]

logger = logging.getLogger(__name__)


class TriageRequest(BaseModel):
    model_config = ConfigDict(extra="forbid", str_strip_whitespace=True)

    event_type: str = Field(min_length=1)
    business_event_type: str | None = Field(default=None, min_length=1)
    source: str = Field(min_length=1)
    destination: str = Field(min_length=1)
    http_status: int | None = Field(default=None, ge=100, le=599)
    error: str = Field(min_length=1, max_length=1000)
    attempt_count: int = Field(ge=1)
    schema_version: str | None = Field(default=None, min_length=1)


class Citation(BaseModel):
    model_config = ConfigDict(extra="forbid")

    runbook_id: str
    chunk_id: str
    title: str
    source_path: str


class TriageDecision(BaseModel):
    model_config = ConfigDict(extra="forbid")

    category: TriageCategory
    summary: str = Field(min_length=1, max_length=500)
    recommended_actions: list[str] = Field(min_length=1, max_length=5)
    redrive_recommendation: RedriveRecommendation
    citations: list[Citation] = Field(min_length=1)
    analysis_mode: AnalysisMode = "deterministic_runbook"
    provider: ProviderName = "deterministic"
    model: str | None = None

    @field_validator("recommended_actions")
    @classmethod
    def _validate_actions(cls, actions: list[str]) -> list[str]:
        for action in actions:
            if not action.strip():
                raise ValueError("recommended actions must be nonempty")
            if len(action) > 250:
                raise ValueError("recommended actions must be 250 characters or fewer")
        return actions


class LLMTriageDraft(BaseModel):
    model_config = ConfigDict(extra="forbid")

    category: TriageCategory
    summary: str = Field(min_length=1, max_length=500)
    recommended_actions: list[str] = Field(min_length=1, max_length=5)
    redrive_recommendation: RedriveRecommendation
    citation_chunk_ids: list[str] = Field(min_length=1, max_length=3)

    @field_validator("recommended_actions")
    @classmethod
    def _validate_actions(cls, actions: list[str]) -> list[str]:
        for action in actions:
            if not action.strip():
                raise ValueError("recommended actions must be nonempty")
            if len(action) > 250:
                raise ValueError("recommended actions must be 250 characters or fewer")
        return actions

    @field_validator("citation_chunk_ids")
    @classmethod
    def _validate_citation_ids(cls, citation_ids: list[str]) -> list[str]:
        if len(citation_ids) != len(set(citation_ids)):
            raise ValueError("citation IDs must not contain duplicates")
        for citation_id in citation_ids:
            if not citation_id.strip():
                raise ValueError("citation IDs must be nonempty")
        return citation_ids


class TriageEngine:
    def __init__(
        self,
        *,
        provider: object,
        fallback_provider: object | None = None,
        runbooks_dir: Path | None = None,
        embedding_provider: EmbeddingProvider | None = None,
        load_chunks: Callable[[Path], Sequence[RunbookChunk]] = load_and_chunk_runbooks,
        build_index_fn: Callable[
            [Sequence[RunbookChunk], EmbeddingProvider], tuple[IndexedChunk, ...]
        ] = build_index,
    ) -> None:
        self._provider = provider
        self._fallback_provider = fallback_provider
        self._embedding_provider = embedding_provider or DeterministicHashEmbeddingProvider(
            dimensions=128
        )
        self._runbooks_dir = runbooks_dir or default_runbooks_dir()
        chunks = tuple(load_chunks(self._runbooks_dir))
        self._index = build_index_fn(chunks, self._embedding_provider)

    @property
    def index(self) -> tuple[IndexedChunk, ...]:
        return self._index

    def triage(self, request: TriageRequest) -> TriageDecision:
        retrieved = retrieve(
            _retrieval_query(request),
            self._index,
            self._embedding_provider,
            top_k=3,
        )
        try:
            decision = self._provider.generate(request, retrieved)  # type: ignore[attr-defined]
            if not isinstance(decision, TriageDecision):
                decision = TriageDecision.model_validate(decision)
            _validate_decision(decision, retrieved)
        except Exception:  # noqa: BLE001 - unsafe provider output must degrade safely.
            if self._fallback_provider is not None:
                return self._fallback(request, retrieved)
            return safe_unknown_fallback(retrieved)
        return decision

    def _fallback(
        self,
        request: TriageRequest,
        retrieved: Sequence[RetrievalResult],
    ) -> TriageDecision:
        provider_name = getattr(self._provider, "provider_name", "unknown")
        model_name = getattr(self._provider, "model", None)
        logger.warning(
            "triage provider failed; using deterministic fallback provider=%s model=%s",
            provider_name,
            model_name,
        )
        try:
            decision = self._fallback_provider.generate(request, retrieved)  # type: ignore[union-attr]
            if not isinstance(decision, TriageDecision):
                decision = TriageDecision.model_validate(decision)
            decision = decision.model_copy(
                update={
                    "analysis_mode": "deterministic_fallback",
                    "provider": "deterministic",
                    "model": None,
                }
            )
            _validate_decision(decision, retrieved)
            return decision
        except Exception:  # noqa: BLE001 - deterministic safety fallback must not 500.
            fallback = safe_unknown_fallback(retrieved)
            return fallback.model_copy(update={"analysis_mode": "deterministic_fallback"})


def create_default_engine() -> TriageEngine:
    from eventrail_ai.triage_provider import create_engine_from_environment

    return create_engine_from_environment()


def default_runbooks_dir() -> Path:
    return Path(__file__).resolve().parents[2] / "runbooks"


def citation_from_result(result: RetrievalResult) -> Citation:
    chunk = result.chunk
    return Citation(
        runbook_id=chunk.runbook_id,
        chunk_id=chunk.chunk_id,
        title=chunk.title,
        source_path=chunk.source_path,
    )


def citations_from_results(results: Sequence[RetrievalResult]) -> list[Citation]:
    return [citation_from_result(result) for result in results]


def safe_unknown_fallback(retrieved: Sequence[RetrievalResult]) -> TriageDecision:
    return TriageDecision(
        category="unknown",
        summary="The cause could not be determined confidently from the sanitized failure metadata.",
        recommended_actions=[
            "Review the sanitized delivery metadata with the destination owner.",
            "Verify destination health, route, authentication, and schema before redrive.",
        ],
        redrive_recommendation="review_required",
        citations=citations_from_results(retrieved[:1]),
    )


def _retrieval_query(request: TriageRequest) -> str:
    fields = [
        request.event_type,
        request.business_event_type or "",
        request.source,
        request.destination,
        str(request.http_status) if request.http_status is not None else "network failure",
        request.error,
        f"attempt_count {request.attempt_count}",
        request.schema_version or "",
    ]
    return " ".join(field for field in fields if field)


def _validate_decision(
    decision: TriageDecision,
    retrieved: Sequence[RetrievalResult],
) -> None:
    retrieved_citations = {
        (
            result.chunk.runbook_id,
            result.chunk.chunk_id,
            result.chunk.title,
            result.chunk.source_path,
        )
        for result in retrieved
    }
    for citation in decision.citations:
        citation_key = (
            citation.runbook_id,
            citation.chunk_id,
            citation.title,
            citation.source_path,
        )
        if citation_key not in retrieved_citations:
            raise ValueError("citation was not retrieved")
    if _contains_forbidden_phrase(decision):
        raise ValueError("unsafe recommendation")


def _contains_forbidden_phrase(decision: TriageDecision) -> bool:
    haystack = " ".join(
        [
            decision.summary,
            decision.redrive_recommendation,
            *decision.recommended_actions,
        ]
    ).lower()
    forbidden = (
        "automatically redrive",
        "autonomous redrive",
        "bypass validation",
        "ignore authentication",
        "disable authentication",
        "skip authorization",
        "send credentials",
    )
    return any(phrase in haystack for phrase in forbidden)


def retrieved_citation_from_chunk_id(
    chunk_id: str,
    retrieved: Sequence[RetrievalResult],
) -> Citation:
    for result in retrieved:
        if result.chunk.chunk_id == chunk_id:
            return citation_from_result(result)
    raise ValueError(f"citation chunk ID was not retrieved: {chunk_id}")
