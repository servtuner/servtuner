"""Dump SGLang's own server arguments into bench's knob catalog.

Names, kinds, defaults, choices, aliases and help come from SGLang's source at the pinned tag, never
written by hand. SGLang declares every server argument as a field of a namespace class in
python/sglang/srt/arg_groups/fields/ (``name: A[type, "help" | Arg(...)] = default``) and builds its
parser from them (arg_utils.add_cli_args_from_dataclass), so the fields are read with ``ast``, without
importing SGLang or torch. Dev-time only: `just sglang-catalog` runs it against a checkout at the tag.
CI only checks the checked-in result.

    python3 -I dump.py <sglang checkout> <version> > sglang_catalog.json
"""

import ast
import json
import subprocess
import sys
from pathlib import Path
from typing import Any

FIELDS_DIR = Path("python/sglang/srt/arg_groups/fields")
CHOICES_FILE = Path("python/sglang/srt/arg_groups/choices.py")
SERVER_ARGS_FILE = Path("python/sglang/srt/server_args.py")
# type_parser names that read one size ("4k", "1g") or one JSON document.
SIZE_PARSERS = {"human_readable_int"}
SCALAR_KINDS = {"int": "integer", "float": "number", "str": "text"}


def main() -> None:
    checkout, version = Path(sys.argv[1]), sys.argv[2]
    constants = module_constants(checkout / CHOICES_FILE)
    flags: list[dict[str, Any]] = []
    other: list[str] = []
    for path in sorted((checkout / FIELDS_DIR).glob("*.py")):
        tree = ast.parse(path.read_text())
        file_constants = {**constants, **module_constants_of(tree)}
        for node in tree.body:
            if isinstance(node, ast.ClassDef):
                for item in node.body:
                    entry = field_entry(node.name, item, file_constants)
                    if entry is None:
                        continue
                    if entry.pop("is_knob"):
                        flags.append(entry)
                    else:
                        other.extend([entry["flag"], *entry["aliases"]])
    other.extend(manual_flags(checkout / SERVER_ARGS_FILE))
    commit = subprocess.run(
        ["git", "-C", str(checkout), "rev-parse", "HEAD"], capture_output=True, text=True, check=True
    ).stdout.strip()
    json.dump(
        {
            "sglang_version": version,
            "sglang_commit": commit,
            "flags": flags,
            "other_flags": sorted(set(other) - {flag["flag"] for flag in flags}),
        },
        sys.stdout,
        indent=1,
        ensure_ascii=False,
    )
    sys.stdout.write("\n")


def module_constants(path: Path) -> dict[str, Any]:
    return module_constants_of(ast.parse(path.read_text())) if path.exists() else {}


def module_constants_of(tree: ast.Module) -> dict[str, Any]:
    """Module-level NAME = <literal> assignments, for choices given by name."""
    constants: dict[str, Any] = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            try:
                constants[node.targets[0].id] = ast.literal_eval(node.value)
            except ValueError:
                continue
    return constants


def field_entry(group: str, item: ast.stmt, constants: dict[str, Any]) -> dict[str, Any] | None:
    if not (isinstance(item, ast.AnnAssign) and isinstance(item.target, ast.Name)):
        return None
    name = item.target.id
    annotation = item.annotation
    if name.startswith("_") or not (isinstance(annotation, ast.Subscript) and dotted(annotation.value) == "A"):
        return None  # bookkeeping, or a Derived field: not a CLI input
    parts = annotation.slice.elts if isinstance(annotation.slice, ast.Tuple) else [annotation.slice]
    field_type, meta = parts[0], parts[1] if len(parts) > 1 else None
    arg = keywords(meta) if isinstance(meta, ast.Call) and dotted(meta.func) == "Arg" else {}
    if literal(arg.get("no_cli")) is True:
        return None
    help_text = text_of(meta) if isinstance(meta, (ast.Constant, ast.JoinedStr)) else text_of(arg.get("help"))
    flag = literal(arg.get("cli_name")) or "--" + name.replace("_", "-")
    aliases = literal(arg.get("aliases")) or []
    choices = choices_of(arg.get("choices"), constants)
    kind, choices = kind_of(unwrap_optional(field_type), arg, choices)
    default = literal(item.value) if item.value is not None else None
    return {
        "is_knob": kind is not None,
        "flag": flag,
        "aliases": aliases,
        "negative_flags": [],
        "dest": name,
        "group": group,
        "kind": kind or "other",
        "default": default if is_json_scalar(default) else None,
        "choices": choices,
        "suggestions": None,
        "help": help_text,
        "limits": None,
    }


def kind_of(node: ast.expr, arg: dict[str, ast.expr], choices: list[Any] | None) -> tuple[str | None, list[Any] | None]:
    """The knob kind of a field's type, or None when it is not one value (a list, a dict, JSON)."""
    if arg.get("nargs") is not None or arg.get("action") is not None:
        return None, None
    parser = dotted(arg["type_parser"]) if "type_parser" in arg else ""
    if parser:
        return ("size", None) if parser.split(".")[-1] in SIZE_PARSERS else (None, None)
    if isinstance(node, ast.Subscript) and dotted(node.value) in ("Literal", "typing.Literal"):
        values = node.slice.elts if isinstance(node.slice, ast.Tuple) else [node.slice]
        return "choice", [literal(value) for value in values]
    name = dotted(node)
    if name == "bool":
        return "switch", None  # SGLang declares booleans as store_true
    if name in SCALAR_KINDS:
        if choices:
            return "choice", choices
        return SCALAR_KINDS[name], None
    return None, None


def unwrap_optional(node: ast.expr) -> ast.expr:
    if isinstance(node, ast.Subscript) and dotted(node.value) in ("Optional", "typing.Optional"):
        return node.slice
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.BitOr):
        for side in (node.left, node.right):
            if not (isinstance(side, ast.Constant) and side.value is None):
                return side
    return node


def choices_of(node: ast.expr | None, constants: dict[str, Any]) -> list[Any] | None:
    if node is None:
        return None
    value = literal(node)
    if value is None and isinstance(node, ast.Name):
        value = constants.get(node.id)
    if isinstance(value, (list, tuple, set)) and all(is_json_scalar(v) for v in value):
        return list(value) if not isinstance(value, set) else sorted(value)
    return None


def manual_flags(path: Path) -> list[str]:
    """Flags ServerArgs.add_cli_args registers by hand (dynamic choices, deprecated flags, --config)."""
    flags: list[str] = []
    for node in ast.walk(ast.parse(path.read_text())):
        if isinstance(node, ast.Call) and dotted(node.func).endswith("add_argument"):
            flags.extend(a.value for a in node.args if isinstance(a, ast.Constant) and str(a.value).startswith("-"))
    return flags


def keywords(call: ast.Call) -> dict[str, ast.expr]:
    return {keyword.arg: keyword.value for keyword in call.keywords if keyword.arg}


def dotted(node: ast.expr | None) -> str:
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute):
        return f"{dotted(node.value)}.{node.attr}"
    return ""


def literal(node: ast.expr | None) -> Any:
    if node is None:
        return None
    try:
        return ast.literal_eval(node)
    except (ValueError, SyntaxError):
        return None


def text_of(node: ast.expr | None) -> str:
    """A help string; an f-string keeps its literal parts and shows each placeholder as its source."""
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.JoinedStr):
        return "".join(
            part.value if isinstance(part, ast.Constant) else "{" + ast.unparse(part.value) + "}"
            for part in node.values
        )
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return text_of(node.left) + text_of(node.right)  # "text" + f"{...}"
    return ""


def is_json_scalar(value: Any) -> bool:
    return value is None or isinstance(value, (bool, int, float, str))


if __name__ == "__main__":
    main()
