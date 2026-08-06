from __future__ import annotations

import json
import shutil
from pathlib import Path

import pytest

from eventrail_ai.runbooks import (
    RunbookDocument,
    chunk_runbook,
    load_and_chunk_runbooks,
    load_runbooks,
)

RUNBOOKS_DIR = Path(__file__).resolve().parents[1] / "runbooks"


def test_every_indexed_runbook_loads_successfully() -> None:
    documents = load_runbooks(RUNBOOKS_DIR)

    assert [document.runbook_id for document in documents] == [
        "receipt-validation-v1",
        "authentication-v1",
        "rate-limiting-v1",
        "destination-outage-v1",
        "schema-errors-v1",
        "routing-configuration-v1",
    ]
    assert all(document.content for document in documents)


def test_missing_files_are_rejected(tmp_path: Path) -> None:
    runbooks_dir = copy_runbooks(tmp_path)
    index = json.loads((runbooks_dir / "index.json").read_text(encoding="utf-8"))
    index[0]["file"] = "missing.md"
    (runbooks_dir / "index.json").write_text(json.dumps(index), encoding="utf-8")

    with pytest.raises(ValueError, match="runbook file not found"):
        load_runbooks(runbooks_dir)


def test_duplicate_runbook_ids_are_rejected(tmp_path: Path) -> None:
    runbooks_dir = copy_runbooks(tmp_path)
    index = json.loads((runbooks_dir / "index.json").read_text(encoding="utf-8"))
    index[1]["runbook_id"] = index[0]["runbook_id"]
    (runbooks_dir / "index.json").write_text(json.dumps(index), encoding="utf-8")

    with pytest.raises(ValueError, match="duplicate runbook_id"):
        load_runbooks(runbooks_dir)


def test_repeated_loading_produces_identical_documents_and_ordering() -> None:
    assert load_runbooks(RUNBOOKS_DIR) == load_runbooks(RUNBOOKS_DIR)


def test_repeated_chunking_produces_identical_ids_and_content() -> None:
    document = load_runbooks(RUNBOOKS_DIR)[0]

    first = chunk_runbook(document)
    second = chunk_runbook(document)

    assert [(chunk.chunk_id, chunk.content) for chunk in first] == [
        (chunk.chunk_id, chunk.content) for chunk in second
    ]


def test_crlf_and_lf_content_produce_same_chunks() -> None:
    content = "# Title\n\nIntro\n\n## Checks\n\nLine one\nLine two\n"
    document_lf = RunbookDocument(
        runbook_id="test-v1",
        title="Test",
        version="1",
        categories=("test",),
        source_path="test.md",
        content=content,
    )
    document_crlf = RunbookDocument(
        runbook_id="test-v1",
        title="Test",
        version="1",
        categories=("test",),
        source_path="test.md",
        content=content.replace("\n", "\r\n"),
    )

    assert chunk_runbook(document_lf) == chunk_runbook(document_crlf)


def test_chunk_ids_are_unique_across_corpus() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)
    chunk_ids = [chunk.chunk_id for chunk in chunks]

    assert len(chunk_ids) == len(set(chunk_ids))


def test_duplicate_headings_receive_deterministic_suffixes() -> None:
    document = RunbookDocument(
        runbook_id="duplicate-v1",
        title="Duplicate",
        version="1",
        categories=("test",),
        source_path="duplicate.md",
        content="## Checks\n\nFirst\n\n## Checks\n\nSecond\n\n## Checks\n\nThird",
    )

    assert [chunk.chunk_id for chunk in chunk_runbook(document)] == [
        "duplicate-v1/checks",
        "duplicate-v1/checks-2",
        "duplicate-v1/checks-3",
    ]


def test_empty_sections_are_skipped() -> None:
    document = RunbookDocument(
        runbook_id="empty-v1",
        title="Empty",
        version="1",
        categories=("test",),
        source_path="empty.md",
        content="## Empty\n\n   \n\n## Present\n\nUse this.",
    )

    chunks = chunk_runbook(document)

    assert [chunk.heading for chunk in chunks] == ["Present"]


def test_every_chunk_contains_citation_metadata() -> None:
    chunks = load_and_chunk_runbooks(RUNBOOKS_DIR)

    for chunk in chunks:
        assert chunk.runbook_id
        assert chunk.chunk_id
        assert chunk.version
        assert chunk.source_path


def test_corpus_covers_required_failure_categories() -> None:
    documents = load_runbooks(RUNBOOKS_DIR)
    categories = {category for document in documents for category in document.categories}

    assert {
        "destination_validation_error",
        "missing_required_field",
        "authentication_error",
        "authorization_error",
        "rate_limited",
        "destination_outage",
        "timeout",
        "connection_failure",
        "destination_5xx",
        "malformed_json",
        "unsupported_schema_version",
        "invalid_field_type",
        "routing_configuration_error",
        "not_found",
        "destination_url_error",
    } <= categories


def test_no_runbook_recommends_autonomous_redrive() -> None:
    forbidden_phrases = (
        "autonomous redrive",
        "automatic redrive",
        "automatically redrive",
        "auto-redrive",
    )

    for path in RUNBOOKS_DIR.glob("*.md"):
        content = path.read_text(encoding="utf-8").lower()
        assert all(phrase not in content for phrase in forbidden_phrases), path


def copy_runbooks(tmp_path: Path) -> Path:
    target = tmp_path / "runbooks"
    shutil.copytree(RUNBOOKS_DIR, target)
    return target
