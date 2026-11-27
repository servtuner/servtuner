"""ULIDs, run tokens, the file BundleStore."""

import re
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from servtuner_api.bundles import BundlePathError, FileBundleStore, read_bundle_json
from servtuner_api.ids import ULID_PATTERN, new_ulid
from servtuner_api.tokens import hash_run_token, new_run_token, run_token_matches


def test_ulids_are_valid_unique_and_time_ordered() -> None:
    earlier, later = new_ulid(1_000), new_ulid(2_000)
    assert ULID_PATTERN.match(earlier) and ULID_PATTERN.match(later)
    assert earlier < later
    assert new_ulid() != new_ulid()


def test_known_ulid_timestamp_prefix() -> None:
    # 2026-10-28T00:00:00Z in milliseconds, encoded as the 10-character time part.
    assert re.fullmatch(r"01[0-9A-Z]{8}[0-9A-Z]{16}", new_ulid(1_792_972_800_000))


def test_token_match_expiry_and_scope() -> None:
    token = new_run_token()
    stored, future = hash_run_token(token), datetime.now(UTC) + timedelta(hours=1)
    assert run_token_matches(token, stored, future)
    assert not run_token_matches(new_run_token(), stored, future)
    assert not run_token_matches(token, stored, datetime.now(UTC) - timedelta(seconds=1))
    assert token not in stored


def test_file_bundle_store_reads_under_the_data_dir(tmp_path: Path) -> None:
    store = FileBundleStore(tmp_path)
    bundle = tmp_path / "runs" / "RUN"
    (bundle / "charts").mkdir(parents=True)
    (bundle / "summary.json").write_text('{"a": 1}')
    (bundle / "charts" / "x.json").write_text("{}")
    key = store.key("RUN")
    assert key == "runs/RUN/"
    assert store.uri(key) == (bundle.as_uri() + "/")
    assert read_bundle_json(store, key, "summary.json") == {"a": 1}
    assert store.get(key, "missing.json") is None
    assert [file.path for file in store.list_files(key)] == ["charts/x.json", "summary.json"]
    assert store.list_files(store.key("OTHER")) == []


def test_file_bundle_store_refuses_to_leave_the_bundle(tmp_path: Path) -> None:
    (tmp_path / "secret").write_text("x")
    with pytest.raises(BundlePathError):
        FileBundleStore(tmp_path).get("runs/RUN/", "../../secret")
    # A key from the database is checked too.
    with pytest.raises(BundlePathError):
        FileBundleStore(tmp_path / "data").get("../", "secret")
