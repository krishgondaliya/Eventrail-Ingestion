from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class RunbookDocument:
    runbook_id: str
    title: str
    version: str
    categories: tuple[str, ...]
    source_path: str
    content: str


@dataclass(frozen=True)
class RunbookChunk:
    runbook_id: str
    chunk_id: str
    title: str
    heading: str
    version: str
    categories: tuple[str, ...]
    source_path: str
    content: str


_HEADING_RE = re.compile(r"^##\s+(.+?)\s*$", re.MULTILINE)
_SLUG_NON_ALNUM_RE = re.compile(r"[^a-z0-9]+")


def load_runbooks(runbooks_dir: Path) -> list[RunbookDocument]:
    index_path = runbooks_dir / "index.json"
    try:
        raw_entries = json.loads(index_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ValueError(f"runbook index not found: {index_path}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"runbook index is invalid JSON: {index_path}") from exc

    if not isinstance(raw_entries, list):
        raise TypeError("runbook index must contain a JSON array")

    documents: list[RunbookDocument] = []
    seen_ids: set[str] = set()
    for position, raw_entry in enumerate(raw_entries):
        if not isinstance(raw_entry, dict):
            raise TypeError(f"runbook index entry {position} must be an object")
        entry = _validate_entry(raw_entry, position)

        runbook_id = entry["runbook_id"]
        if runbook_id in seen_ids:
            raise ValueError(f"duplicate runbook_id: {runbook_id}")
        seen_ids.add(runbook_id)

        markdown_path = runbooks_dir / entry["file"]
        if not markdown_path.is_file():
            raise ValueError(f"runbook file not found for {runbook_id}: {entry['file']}")

        documents.append(
            RunbookDocument(
                runbook_id=runbook_id,
                title=entry["title"],
                version=entry["version"],
                categories=tuple(entry["categories"]),
                source_path=entry["file"],
                content=_normalize_markdown(markdown_path.read_text(encoding="utf-8")),
            )
        )

    return documents


def chunk_runbook(document: RunbookDocument) -> list[RunbookChunk]:
    content = _normalize_markdown(document.content)
    matches = list(_HEADING_RE.finditer(content))
    sections: list[tuple[str, str]] = []

    if not matches:
        if content:
            sections.append(("overview", content))
    else:
        overview = content[: matches[0].start()].strip()
        if overview:
            sections.append(("overview", overview))

        for idx, match in enumerate(matches):
            heading = match.group(1).strip()
            section_start = match.end()
            section_end = matches[idx + 1].start() if idx + 1 < len(matches) else len(content)
            section_content = content[section_start:section_end].strip()
            if section_content:
                sections.append((heading, section_content))

    slug_counts: dict[str, int] = {}
    chunks: list[RunbookChunk] = []
    for heading, section_content in sections:
        base_slug = _slugify(heading)
        slug_counts[base_slug] = slug_counts.get(base_slug, 0) + 1
        slug = base_slug if slug_counts[base_slug] == 1 else f"{base_slug}-{slug_counts[base_slug]}"
        chunks.append(
            RunbookChunk(
                runbook_id=document.runbook_id,
                chunk_id=f"{document.runbook_id}/{slug}",
                title=document.title,
                heading=heading,
                version=document.version,
                categories=document.categories,
                source_path=document.source_path,
                content=section_content,
            )
        )

    return chunks


def load_and_chunk_runbooks(runbooks_dir: Path) -> list[RunbookChunk]:
    chunks: list[RunbookChunk] = []
    for document in load_runbooks(runbooks_dir):
        chunks.extend(chunk_runbook(document))
    return chunks


def _validate_entry(raw_entry: dict[str, Any], position: int) -> dict[str, Any]:
    runbook_id = _required_string(raw_entry, "runbook_id", position)
    title = _required_string(raw_entry, "title", position)
    version = _required_string(raw_entry, "version", position)
    file_name = _required_string(raw_entry, "file", position)

    categories = raw_entry.get("categories")
    if not isinstance(categories, list) or not categories:
        raise ValueError(f"runbook index entry {position} categories must be a nonempty array")

    validated_categories: list[str] = []
    seen_categories: set[str] = set()
    for category in categories:
        if not isinstance(category, str) or not category.strip():
            raise ValueError(f"runbook index entry {position} categories must be nonempty strings")
        value = category.strip()
        if value in seen_categories:
            raise ValueError(f"duplicate category for {runbook_id}: {value}")
        seen_categories.add(value)
        validated_categories.append(value)

    return {
        "runbook_id": runbook_id,
        "title": title,
        "version": version,
        "file": file_name,
        "categories": validated_categories,
    }


def _required_string(raw_entry: dict[str, Any], key: str, position: int) -> str:
    value = raw_entry.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"runbook index entry {position} {key} must be a nonempty string")
    return value.strip()


def _normalize_markdown(content: str) -> str:
    return content.replace("\r\n", "\n").replace("\r", "\n").strip()


def _slugify(value: str) -> str:
    slug = _SLUG_NON_ALNUM_RE.sub("-", value.lower()).strip("-")
    return slug or "section"
