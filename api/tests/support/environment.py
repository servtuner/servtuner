"""Environment dictionaries for ``Settings.from_env`` in tests."""

from pathlib import Path


def platform_env(tmp_path: Path, **overrides: str) -> dict[str, str]:
    """A valid all-``platform`` configuration; tests override single names."""
    bench_binary = tmp_path / "bench"
    bench_binary.touch()
    env = {
        "SERVTUNER_TLS_MODE": "platform",
        "SERVTUNER_DATABASE_HOST": "db.example.invalid",
        "SERVTUNER_DATABASE_NAME": "servtuner",
        "SERVTUNER_DATABASE_USER": "servtuner",
        "SERVTUNER_DATABASE_PASSWORD": "not-a-real-password",
        "SERVTUNER_DATABASE_SSLMODE": "disable",
        "SERVTUNER_BENCH_BINARY": str(bench_binary),
        "SERVTUNER_DATA_DIR": str(tmp_path / "data"),
    }
    return env | overrides
