from __future__ import annotations

import math
from collections.abc import Sequence
from dataclasses import dataclass

from eventrail_ai.embeddings import EmbeddingProvider
from eventrail_ai.runbooks import RunbookChunk


@dataclass(frozen=True)
class IndexedChunk:
    chunk: RunbookChunk
    embedding: tuple[float, ...]


@dataclass(frozen=True)
class RetrievalResult:
    chunk: RunbookChunk
    score: float


def build_index(
    chunks: Sequence[RunbookChunk],
    provider: EmbeddingProvider,
) -> tuple[IndexedChunk, ...]:
    _validate_dimensions(provider.dimensions)
    texts = [_chunk_embedding_text(chunk) for chunk in chunks]
    embeddings = provider.embed_texts(texts)
    if len(embeddings) != len(chunks):
        raise ValueError("embedding provider returned the wrong number of vectors")

    indexed: list[IndexedChunk] = []
    for chunk, embedding in zip(chunks, embeddings, strict=True):
        _validate_embedding(embedding, provider.dimensions)
        indexed.append(IndexedChunk(chunk=chunk, embedding=tuple(embedding)))
    return tuple(indexed)


def retrieve(
    query: str,
    index: Sequence[IndexedChunk],
    provider: EmbeddingProvider,
    top_k: int = 3,
) -> tuple[RetrievalResult, ...]:
    if top_k <= 0:
        raise ValueError("top_k must be positive")
    _validate_dimensions(provider.dimensions)
    if not query.strip():
        return ()

    query_embeddings = provider.embed_texts([query])
    if len(query_embeddings) != 1:
        raise ValueError("embedding provider returned the wrong number of query vectors")
    query_embedding = query_embeddings[0]
    _validate_embedding(query_embedding, provider.dimensions)

    results: list[RetrievalResult] = []
    for indexed in index:
        _validate_embedding(indexed.embedding, provider.dimensions)
        results.append(
            RetrievalResult(
                chunk=indexed.chunk,
                score=_cosine_similarity(query_embedding, indexed.embedding),
            )
        )

    results.sort(key=lambda result: (-result.score, result.chunk.chunk_id))
    return tuple(results[:top_k])


def _chunk_embedding_text(chunk: RunbookChunk) -> str:
    return "\n".join(
        [
            f"Title: {chunk.title}",
            f"Heading: {chunk.heading}",
            f"Categories: {', '.join(chunk.categories)}",
            f"Content: {chunk.content}",
        ]
    )


def _cosine_similarity(left: Sequence[float], right: Sequence[float]) -> float:
    left_norm = math.sqrt(sum(value * value for value in left))
    right_norm = math.sqrt(sum(value * value for value in right))
    if left_norm == 0 or right_norm == 0:
        return 0.0
    dot = sum(left_value * right_value for left_value, right_value in zip(left, right, strict=True))
    return dot / (left_norm * right_norm)


def _validate_dimensions(dimensions: int) -> None:
    if dimensions <= 0:
        raise ValueError("embedding dimensions must be positive")


def _validate_embedding(embedding: Sequence[float], dimensions: int) -> None:
    if len(embedding) != dimensions:
        raise ValueError("embedding dimensions do not match provider dimensions")
