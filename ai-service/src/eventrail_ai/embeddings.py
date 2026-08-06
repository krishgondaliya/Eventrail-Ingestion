from __future__ import annotations

import hashlib
import math
import re
from collections.abc import Sequence
from typing import Protocol


class EmbeddingProvider(Protocol):
    @property
    def dimensions(self) -> int:
        ...

    def embed_texts(
        self,
        texts: Sequence[str],
    ) -> list[tuple[float, ...]]:
        ...


class DeterministicHashEmbeddingProvider:
    def __init__(self, dimensions: int = 128) -> None:
        if dimensions <= 0:
            raise ValueError("embedding dimensions must be positive")
        self._dimensions = dimensions

    @property
    def dimensions(self) -> int:
        return self._dimensions

    def embed_texts(
        self,
        texts: Sequence[str],
    ) -> list[tuple[float, ...]]:
        return [self._embed_text(text) for text in texts]

    def _embed_text(self, text: str) -> tuple[float, ...]:
        vector = [0.0] * self._dimensions
        tokens = _tokenize(text)
        if not tokens:
            return tuple(vector)

        for token in tokens:
            digest = hashlib.sha256(token.encode("utf-8")).digest()
            index = int.from_bytes(digest[:8], byteorder="big") % self._dimensions
            sign = 1.0 if digest[8] & 1 == 0 else -1.0
            vector[index] += sign

        norm = math.sqrt(sum(value * value for value in vector))
        if norm == 0:
            return tuple(vector)
        return tuple(value / norm for value in vector)


_TOKEN_RE = re.compile(r"[a-z0-9_]+")


def _tokenize(text: str) -> tuple[str, ...]:
    return tuple(_TOKEN_RE.findall(text.lower()))
