"""What a username and a password must be."""

from fastapi import status

from servtuner_api.auth.guard import refusal
from servtuner_api.auth.passwords import PasswordPolicyError, check_policy

USERNAME_MAX_LENGTH = 128


USERNAME_CHARACTERS = frozenset(
    "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._@-"
)


def check_username(username: str) -> None:
    if not 1 <= len(username) <= USERNAME_MAX_LENGTH or not set(username) <= USERNAME_CHARACTERS:
        raise refusal(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            "invalid_username",
            f"a username has 1 to {USERNAME_MAX_LENGTH} letters, digits, '.', '_', '@' or '-'",
        )


def check_password(password: str) -> None:
    try:
        check_policy(password)
    except PasswordPolicyError as error:
        raise refusal(status.HTTP_422_UNPROCESSABLE_CONTENT, "weak_password", str(error)) from error
