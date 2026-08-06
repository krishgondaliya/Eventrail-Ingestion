from __future__ import annotations

import math

import pytest

from eventrail_ai.embeddings import DeterministicHashEmbeddingProvider


def test_repeated_embedding_calls_return_identical_vectors() -> None:
    provider = DeterministicHashEmbeddingProvider()
    texts = ["Missing invoice_id required receipt field", "401 unauthorized credentials"]

    assert provider.embed_texts(texts) == provider.embed_texts(texts)


def test_embedding_dimensions_are_correct() -> None:
    provider = DeterministicHashEmbeddingProvider(dimensions=32)

    vectors = provider.embed_texts(["invoice_id", "401", "5xx"])

    assert provider.dimensions == 32
    assert [len(vector) for vector in vectors] == [32, 32, 32]


def test_nonempty_vectors_are_normalized() -> None:
    provider = DeterministicHashEmbeddingProvider()
    vector = provider.embed_texts(["invoice_id amount currency"])[0]

    norm = math.sqrt(sum(value * value for value in vector))

    assert norm == pytest.approx(1.0)


def test_blank_text_produces_zero_vector() -> None:
    provider = DeterministicHashEmbeddingProvider(dimensions=8)

    assert provider.embed_texts([" \n\t "]) == [(0.0,) * 8]


def test_invalid_dimensions_are_rejected() -> None:
    with pytest.raises(ValueError, match="dimensions"):
        DeterministicHashEmbeddingProvider(dimensions=0)

    with pytest.raises(ValueError, match="dimensions"):
        DeterministicHashEmbeddingProvider(dimensions=-1)


def test_input_order_is_preserved() -> None:
    provider = DeterministicHashEmbeddingProvider()
    texts = ["invoice_id", "401", "429", "404", "5xx"]

    vectors = provider.embed_texts(texts)

    assert vectors == [provider.embed_texts([text])[0] for text in texts]
    assert len(set(vectors)) == len(texts)


def test_preserves_operational_tokens_as_similarity_terms() -> None:
    provider = DeterministicHashEmbeddingProvider()

    invoice_vector = provider.embed_texts(["invoice_id"])[0]
    invoice_sentence_vector = provider.embed_texts(["missing invoice_id required field"])[0]
    unrelated_vector = provider.embed_texts(["authentication credentials"])[0]

    assert dot(invoice_vector, invoice_sentence_vector) > dot(invoice_vector, unrelated_vector)


def dot(left: tuple[float, ...], right: tuple[float, ...]) -> float:
    return sum(left_value * right_value for left_value, right_value in zip(left, right, strict=True))
