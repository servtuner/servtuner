"""What the servtuner CLI tells the api when it fails, defined once for every caller."""

# servtuner exits 2 for input it refuses (ExitInvalidSpec, bench/internal/cli/exitcode.go).
EXIT_INVALID = 2
# The prefix servtuner puts on the error it prints last on stderr (bench/internal/cli/cli.go).
ERROR_PREFIX = "servtuner: "


def error_message(line: str) -> str:
    """A servtuner error line as a user should read it: without the CLI's prefix."""
    return line.removeprefix(ERROR_PREFIX)
