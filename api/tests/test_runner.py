"""SubprocessRunner against a stand-in ``bench`` script: the CLI contract, not bench itself."""

import dataclasses
import stat
from pathlib import Path

import pytest

from servtuner_api.runner import (
    LaunchRequest,
    RunnerState,
    SubprocessRunner,
    SubprocessRunnerConfig,
    shard_count,
)
from servtuner_api.secret_store import FileSecretStore, SecretStore
from servtuner_api.settings.models import ConnectionTls
from tests.support.waiting import wait_until

# Records its argv and the token it was handed, then exits as BENCH_EXIT_CODE says.
FAKE_BENCH = """#!/bin/sh
echo "$@" > "$SERVTUNER_DATA_DIR/argv.txt"
echo "$@" >> "$SERVTUNER_DATA_DIR/argv-all.txt"
case "$*" in *--shard-index*) [ -n "$FOLLOWER_SLEEP" ] && exec sleep "$FOLLOWER_SLEEP" ;; esac
while [ $# -gt 0 ]; do
  [ "$1" = "--run-token-file" ] && cat "$2" > "$SERVTUNER_DATA_DIR/token-seen.txt"
  [ "$1" = "--secrets-dir" ] && (cd "$2" && find . -type f | sort) \
    > "$SERVTUNER_DATA_DIR/secrets.txt"
  [ "$1" = "--secrets-dir" ] && cat "$2/vllm-key/token" > "$SERVTUNER_DATA_DIR/key-seen.txt"
  shift
done
echo "bench says hello"
[ -n "$BENCH_SLEEP" ] && sleep "$BENCH_SLEEP"
exit "${BENCH_EXIT_CODE:-0}"
"""
RUN_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

PLAIN = ConnectionTls(enabled=False, client_cert=False)
TLS_ONLY = ConnectionTls(enabled=True, client_cert=False)
MUTUAL = ConnectionTls(enabled=True, client_cert=True)


def make_runner(
    tmp_path: Path,
    connection: ConnectionTls,
    engine_override: str | None = None,
    secret_store: SecretStore | None = None,
) -> SubprocessRunner:
    bench = tmp_path / "bench"
    bench.write_text(FAKE_BENCH)
    bench.chmod(bench.stat().st_mode | stat.S_IXUSR)
    data_dir = tmp_path / "data"
    data_dir.mkdir(exist_ok=True)
    return SubprocessRunner(
        SubprocessRunnerConfig(
            bench_binary=bench,
            data_dir=data_dir,
            api_url=(
                "https://api.example.invalid"
                if connection.enabled
                else "http://api.example.invalid"
            ),
            api_connection=connection,
            tls_cert_file=Path("/certs/tls.crt") if connection.client_cert else None,
            tls_key_file=Path("/certs/tls.key") if connection.client_cert else None,
            engine_override=engine_override,
            run_secrets_dir=tmp_path / "run-secrets",
            mounted_secrets_dir=tmp_path / "mounted",
        ),
        secret_store,
    )


def run_to_completion(runner: SubprocessRunner) -> tuple[str, RunnerState]:
    handle = runner.launch(LaunchRequest(RUN_ID, b'{"spec": true}', "secret-token"))
    wait_until(lambda: runner.status(handle).is_final, "bench to exit")
    return handle.reference, runner.status(handle).state


@pytest.mark.parametrize("connection", [PLAIN, TLS_ONLY, MUTUAL])
def test_command_line_follows_the_contract(tmp_path: Path, connection: ConnectionTls) -> None:
    runner = make_runner(tmp_path, connection)
    run_to_completion(runner)
    argv = (tmp_path / "data" / "argv.txt").read_text().split()
    data_dir = tmp_path / "data"
    expected = [
        "run", "--spec", str(data_dir / "specs" / RUN_ID),
        "--out", str(data_dir / "runs" / RUN_ID), "--run-id", RUN_ID,
        "--api-url", runner._config.api_url,
        "--run-token-file", str(data_dir / "tokens" / RUN_ID),
        "--api-tls", "true" if connection.enabled else "false",
    ]  # fmt: skip
    if connection.client_cert:
        expected += ["--tls-cert-file", "/certs/tls.crt", "--tls-key-file", "/certs/tls.key"]
    assert argv == expected


def test_engine_override_is_passed_only_when_set(tmp_path: Path) -> None:
    spec_path, token_path = tmp_path / "spec", tmp_path / "token"
    without = make_runner(tmp_path, PLAIN).build_command(RUN_ID, spec_path, token_path)
    with_override = make_runner(tmp_path, PLAIN, engine_override="null").build_command(
        RUN_ID, spec_path, token_path
    )
    assert "--engine-override" not in without
    position = with_override.index("--engine-override")
    assert with_override[position : position + 2] == ["--engine-override", "null"]


def test_an_offline_install_runs_bench_offline(tmp_path: Path) -> None:
    runner = make_runner(tmp_path, PLAIN)
    assert "--offline" not in runner.build_command(RUN_ID, tmp_path / "spec", tmp_path / "token")
    runner._config = dataclasses.replace(runner._config, offline=True)
    assert "--offline" in runner.build_command(RUN_ID, tmp_path / "spec", tmp_path / "token")


def test_spec_is_written_verbatim_and_token_is_private(tmp_path: Path) -> None:
    runner = make_runner(tmp_path, PLAIN)
    run_to_completion(runner)
    assert (tmp_path / "data" / "specs" / RUN_ID).read_bytes() == b'{"spec": true}'
    assert (tmp_path / "data" / "token-seen.txt").read_text() == "secret-token"
    # Once bench is done the token file is removed, and the spec file was never world-readable.
    assert not (tmp_path / "data" / "tokens" / RUN_ID).exists()
    assert (tmp_path / "data" / "specs" / RUN_ID).stat().st_mode & 0o077 == 0


def test_exit_code_decides_the_final_state(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    runner = make_runner(tmp_path, PLAIN)
    assert run_to_completion(runner)[1] is RunnerState.SUCCEEDED
    monkeypatch.setenv("BENCH_EXIT_CODE", "3")
    handle = runner.launch(LaunchRequest(RUN_ID, b"{}", "t"))
    wait_until(lambda: runner.status(handle).is_final, "bench to exit")
    status = runner.status(handle)
    assert (status.state, status.exit_code) == (RunnerState.FAILED, 3)


def test_logs_and_cancel(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BENCH_SLEEP", "30")
    runner = make_runner(tmp_path, PLAIN)
    handle = runner.launch(LaunchRequest(RUN_ID, b"{}", "t"))
    wait_until(lambda: "bench says hello" in list(runner.logs(handle)), "bench to log")
    assert runner.status(handle).state is RunnerState.RUNNING
    runner.cancel(handle)
    assert runner.status(handle).state is RunnerState.CANCELLED


def test_unknown_handle_is_failed(tmp_path: Path) -> None:
    runner = make_runner(tmp_path, PLAIN)
    handle = runner.launch(LaunchRequest(RUN_ID, b"{}", "t"))
    wait_until(lambda: runner.status(handle).is_final, "bench to exit")
    restarted = make_runner(tmp_path, PLAIN)
    assert restarted.status(handle).state is RunnerState.FAILED


SHARDED_SPEC = b'{"engine": {"type": "loadgen", "shards": 3}}'


def test_shard_count_reads_engine_shards() -> None:
    assert shard_count(SHARDED_SPEC) == 3
    assert shard_count(b'{"engine": {"type": "loadgen"}}') == 1
    assert shard_count(b"{}") == 1


def test_a_sharded_run_starts_every_shard_and_the_leader_decides(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("FOLLOWER_SLEEP", "30")
    runner = make_runner(tmp_path, PLAIN)
    handle = runner.launch(LaunchRequest(RUN_ID, SHARDED_SPEC, "t"))
    argv_all = tmp_path / "data" / "argv-all.txt"
    wait_until(
        lambda: argv_all.exists() and len(argv_all.read_text().splitlines()) == 3, "three shards"
    )
    lines = argv_all.read_text().splitlines()
    assert sorted(
        line.split("--shard-index ")[-1] for line in lines if "--shard-index" in line
    ) == [
        "1",
        "2",
    ]
    assert all(f"--run-id {RUN_ID}" in line for line in lines), "one run id for every shard"
    followers = list(runner._followers[RUN_ID])  # noqa: SLF001 - the test checks the cleanup
    wait_until(lambda: runner.status(handle).is_final, "the leader to exit")
    assert runner.status(handle).state is RunnerState.SUCCEEDED
    assert all(follower.poll() is not None for follower in followers), "followers are stopped"


SPEC_WITH_SECRETS = (
    b'{"target": {"auth": {"secretRef": {"name": "vllm-key", "key": "token"}},'
    b' "admin": {"secretRef": {"name": "chart-mounted", "tokenKey": "token"}},'
    b' "upstreams": [{"auth": {"secretRef": {"name": "nowhere", "key": "token"}}}]}}'
)


def test_a_runs_secrets_are_materialized_privately_and_removed(tmp_path: Path) -> None:
    store = FileSecretStore(tmp_path / "store")
    store.put("vllm-key", {"token": "sk-from-the-store"}, "01TARGET")
    (tmp_path / "mounted" / "chart-mounted").mkdir(parents=True)
    (tmp_path / "mounted" / "chart-mounted" / "token").write_text("from-the-chart")
    runner = make_runner(tmp_path, PLAIN, secret_store=store)

    handle = runner.launch(LaunchRequest(RUN_ID, SPEC_WITH_SECRETS, "t"))
    wait_until(lambda: runner.status(handle).is_final, "bench to exit")

    argv = (tmp_path / "data" / "argv.txt").read_text()
    assert f"--secrets-dir {tmp_path / 'run-secrets' / RUN_ID}" in argv
    # The store's Secret and the chart-mounted one; a Secret found in neither is
    # left for bench to report at resolve.
    seen = (tmp_path / "data" / "secrets.txt").read_text().split()
    assert seen == ["./chart-mounted/token", "./vllm-key/token"]
    assert (tmp_path / "data" / "key-seen.txt").read_text() == "sk-from-the-store"
    assert not (tmp_path / "run-secrets" / RUN_ID).exists(), "removed once bench is done"


def test_a_run_naming_no_secret_gets_no_secrets_dir(tmp_path: Path) -> None:
    runner = make_runner(tmp_path, PLAIN, secret_store=FileSecretStore(tmp_path))
    run_to_completion(runner)
    assert "--secrets-dir" not in (tmp_path / "data" / "argv.txt").read_text()
