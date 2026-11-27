"""Saved targets: a RunSpec target block under a name, its keys
held write-only in the secret store, and Check connection through bench.

Rules that hold everywhere in this module:
- a key arrives as a ``SecretStr`` and is unwrapped only to hand it to the
  secret store, or to write it into a check's private secrets directory;
- no return value, log line or exception message carries a key;
- validation errors name the field and the problem, never the input.

- ``common``: credential slots, errors, masking and body parsing.
- ``checker``: ``servtuner check-target`` and ``servtuner boot-probe`` with a target's keys.
- ``service``: saved targets and their write-only keys (``TargetService``)."""
