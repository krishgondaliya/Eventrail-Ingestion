from __future__ import annotations

import socket
from collections.abc import Sequence
from pathlib import Path

import pytest

from eventrail_ai.embeddings import DeterministicHashEmbeddingProvider, EmbeddingProvider
from eventrail_ai.retrieval import IndexedChunk, build_index, retrieve
from eventrail_ai.runbooks import RunbookChunk, load_and_chunk_runbooks

RUNBOOKS_DIR = Path(__file__).resolve().parents[1] / "runbooks"


def test_repeated_index_construction_is_identical() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()

    assert build_index(chunks, provider) == build_index(chunks, provider)


def test_repeated_retrieval_returns_same_ranking_and_scores() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()
    index = build_index(chunks, provider)

    first = retrieve("missing invoice_id required receipt field", index, provider, top_k=5)
    second = retrieve("missing invoice_id required receipt field", index, provider, top_k=5)

    assert [(result.chunk.chunk_id, result.score) for result in first] == [
        (result.chunk.chunk_id, result.score) for result in second
    ]


def test_results_are_ordered_deterministically_when_scores_tie() -> None:
    provider = DeterministicHashEmbeddingProvider(dimensions=4)
    index = (
        IndexedChunk(chunk=make_chunk("tie-v1/b"), embedding=(0.0, 0.0, 0.0, 0.0)),
        IndexedChunk(chunk=make_chunk("tie-v1/a"), embedding=(0.0, 0.0, 0.0, 0.0)),
        IndexedChunk(chunk=make_chunk("tie-v1/c"), embedding=(0.0, 0.0, 0.0, 0.0)),
    )

    results = retrieve("anything", index, provider, top_k=3)

    assert [result.chunk.chunk_id for result in results] == ["tie-v1/a", "tie-v1/b", "tie-v1/c"]


def test_top_k_is_enforced() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()
    index = build_index(chunks, provider)

    assert len(retrieve("receipt destination", index, provider, top_k=2)) == 2


def test_blank_queries_return_no_results() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()
    index = build_index(chunks, provider)

    assert retrieve(" \n\t", index, provider) == ()


def test_invalid_top_k_is_rejected() -> None:
    provider = DeterministicHashEmbeddingProvider()

    with pytest.raises(ValueError, match="top_k"):
        retrieve("receipt", (), provider, top_k=0)


def test_returned_chunks_retain_runbook_and_citation_metadata() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()
    index = build_index(chunks, provider)

    result = retrieve("missing invoice_id required receipt field", index, provider, top_k=1)[0]

    assert result.chunk.runbook_id
    assert result.chunk.chunk_id
    assert result.chunk.version
    assert result.chunk.source_path
    assert result.chunk.categories


@pytest.mark.parametrize(
    ("query", "want_runbook_id"),
    [
        ("missing invoice_id required receipt field", "receipt-validation-v1"),
        ("401 unauthorized credentials", "authentication-v1"),
        ("429 rate limit retry after", "rate-limiting-v1"),
        ("connection timeout destination 503", "destination-outage-v1"),
        ("malformed JSON unsupported schema version", "schema-errors-v1"),
        ("404 incorrect destination URL", "routing-configuration-v1"),
    ],
)
def test_queries_rank_relevant_runbook_first(query: str, want_runbook_id: str) -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()
    index = build_index(chunks, provider)

    result = retrieve(query, index, provider, top_k=1)[0]

    assert result.chunk.runbook_id == want_runbook_id


def test_no_external_network_or_model_provider_is_used(monkeypatch: pytest.MonkeyPatch) -> None:
    def fail_network(*args: object, **kwargs: object) -> None:
        raise AssertionError("network should not be used")

    monkeypatch.setattr(socket, "create_connection", fail_network)
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    provider = DeterministicHashEmbeddingProvider()
    index = build_index(chunks, provider)

    assert retrieve("invoice_id", index, provider, top_k=1)


def test_build_index_validates_embedding_count() -> None:
    provider = WrongCountProvider()

    with pytest.raises(ValueError, match="wrong number"):
        build_index((make_chunk("count-v1/a"),), provider)


def test_retrieve_validates_embedding_dimensions() -> None:
    provider = DeterministicHashEmbeddingProvider(dimensions=4)
    index = (IndexedChunk(chunk=make_chunk("bad-v1/a"), embedding=(1.0, 0.0)),)

    with pytest.raises(ValueError, match="dimensions"):
        retrieve("receipt", index, provider)


def make_chunk(chunk_id: str) -> RunbookChunk:
    runbook_id = chunk_id.split("/", maxsplit=1)[0]
    return RunbookChunk(
        runbook_id=runbook_id,
        chunk_id=chunk_id,
        title="Tie",
        heading="Tie",
        version="1",
        categories=("test",),
        source_path="tie.md",
        content="same",
    )


class WrongCountProvider:
    @property
    def dimensions(self) -> int:
        return 4

    def embed_texts(self, texts: Sequence[str]) -> list[tuple[float, ...]]:
        return []


def assert_provider_contract(provider: EmbeddingProvider) -> EmbeddingProvider:
    return provider
