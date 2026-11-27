"""Dump vLLM's own `vllm serve` arguments into bench's knob catalog.

Descriptions, types, defaults, choices and limits come from vLLM's source at the pinned version,
never written by hand. Dev-time only: it needs a vLLM checkout
at the pinned tag and a virtualenv with CPU torch and vLLM's requirements/common.txt (about 2.4 GB);
`just vllm-catalog` runs it. CI only checks the checked-in result.

    VLLM_TARGET_DEVICE=cpu python -I dump.py <vllm checkout> <version> > vllm_catalog.json
"""

import argparse
import dataclasses
import enum
import json
import subprocess
import sys
from typing import Any

# Our own arguments, read before vLLM's import sees sys.argv: vLLM fills in help text only when
# --help is on the command line at import time, and skips --model when "serve" is
# (arg_utils.py:285, :977).
ARGS = sys.argv[1:]
sys.argv = ["vllm", "--help"]

# Wrappers that only parse a value; the kind comes from what they wrap or say they read.
SIZE_TYPES = {"human_readable_int", "human_readable_int_or_auto"}
JSON_TYPES = {"loads", "parse_dataclass", "union_dict_and_str"}


def main() -> None:
    checkout, version = ARGS
    sys.path.insert(0, checkout)
    from vllm.entrypoints.common.cli_args import FrontendArgs, create_parser_for_docs

    import vllm.config as config_module

    parser = create_parser_for_docs()
    classes = {
        cls.__name__: cls
        for cls in [*vars(config_module).values(), FrontendArgs]
        if isinstance(cls, type) and dataclasses.is_dataclass(cls)
    }
    flags = []
    for group in parser._action_groups:
        for action in group._group_actions:
            entry = flag_entry(group.title or "", action, classes)
            if entry is not None:
                flags.append(entry)
    commit = subprocess.run(
        ["git", "-C", checkout, "rev-parse", "HEAD"], capture_output=True, text=True, check=True
    ).stdout.strip()
    json.dump(
        {"vllm_version": version, "vllm_commit": commit, "flags": flags},
        sys.stdout,
        indent=1,
        ensure_ascii=False,
    )
    sys.stdout.write("\n")


def flag_entry(
    group: str, action: argparse.Action, classes: dict[str, type]
) -> dict[str, Any] | None:
    if not action.option_strings or action.help is argparse.SUPPRESS:
        return None  # positionals, --help, hidden flags
    kind_name = type(action).__name__
    if kind_name in ("_HelpAction", "DeprecatedLogConfigFileAction", "LoRAParserAction"):
        return None
    # EnableDisableAction knows its own off spellings: --disable-x for an enable_x field, but
    # --enable-x for a disable_x one (argparse_utils.py:120-145).
    off = getattr(action, "negative_option_strings", set())
    negatives = [f for f in action.option_strings if f in off]
    names = [f for f in action.option_strings if f not in negatives]
    entry: dict[str, Any] = {
        "flag": names[0],
        "aliases": names[1:],
        "negative_flags": negatives,
        "dest": action.dest,
        "group": group,
        "kind": kind_of(action),
        "default": native(action.default),
        "choices": [native(c) for c in action.choices] if action.choices else None,
        "suggestions": suggestions(action.metavar),
        "help": clean_help(action),
        "limits": limits_of(classes.get(group), action.dest),
    }
    return entry


def kind_of(action: argparse.Action) -> str:
    name = type(action).__name__
    if name == "EnableDisableAction":
        return "boolean"
    if name == "_StoreTrueAction":
        return "switch"
    if name == "_AppendAction" or action.nargs in ("+", "*"):
        return "list"
    if action.choices:
        return "choice"
    parse = unwrap(action.type)
    type_name = getattr(parse, "__name__", "")
    if type_name in SIZE_TYPES:
        return "size"
    if type_name in JSON_TYPES:
        return "json"
    if parse is int:
        return "integer"
    if parse is float:
        return "number"
    return "text"


def unwrap(parse: Any) -> Any:
    """vLLM's _optional_type(inner) closes over the type it wraps."""
    if getattr(parse, "__name__", "") == "_optional_type" and parse.__closure__:
        for cell in parse.__closure__:
            if callable(cell.cell_contents):
                return unwrap(cell.cell_contents)
    return parse


def native(value: Any) -> Any:
    """JSON values as vLLM means them: an enum as its value, anything else odd as text."""
    if isinstance(value, enum.Enum):
        value = value.value
    if value is None or isinstance(value, (bool, int, float, str)):
        return value
    if isinstance(value, (list, tuple)):
        return [native(v) for v in value]
    if isinstance(value, dict):
        return {str(k): native(v) for k, v in value.items()}
    return str(value)


def suggestions(metavar: Any) -> list[str] | None:
    """A metavar list names known values without enforcing them (--kv-cache-dtype)."""
    if isinstance(metavar, (list, tuple)):
        return [str(v) for v in metavar]
    if isinstance(metavar, str) and metavar.startswith("[") and metavar.endswith("]"):
        try:
            parsed = json.loads(metavar.replace("'", '"'))
        except json.JSONDecodeError:
            return None
        return [str(v) for v in parsed] if isinstance(parsed, list) else None
    return None


def clean_help(action: argparse.Action) -> str:
    text = str(action.help or "")
    doc = getattr(unwrap(action.type), "__doc__", None)
    if doc and getattr(unwrap(action.type), "__name__", "") in SIZE_TYPES:
        text = text.replace(doc, "")  # the size parser's own docstring, appended by vLLM
    return text.replace("%%", "%").strip()


def limits_of(cls: type | None, dest: str) -> dict[str, float] | None:
    """Field(ge/gt/le/lt) on the config class the argparse group is named after."""
    fields = getattr(cls, "__pydantic_fields__", {}) if cls else {}
    field = fields.get(dest)
    if field is None:
        return None
    limits = {
        key: getattr(bound, key)
        for bound in field.metadata
        for key in ("ge", "gt", "le", "lt")
        if hasattr(bound, key)
    }
    return limits or None


if __name__ == "__main__":
    main()
