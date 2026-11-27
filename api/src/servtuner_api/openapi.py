"""Write the OpenAPI document that the frontend's TypeScript client is generated from.

Run by ``just gen``. The output is deterministic (sorted keys) so the root
``gen-check`` can fail when the checked-in ``openapi.json`` is stale.
"""

import json
import sys
from pathlib import Path
from typing import NoReturn

from servtuner_api.app import create_app


def render_openapi() -> str:
    def no_service() -> NoReturn:
        raise RuntimeError("generating the OpenAPI document never starts the service")

    app = create_app(no_service)
    return json.dumps(app.openapi(), indent=2, sort_keys=True) + "\n"


def main() -> None:
    Path(sys.argv[1]).write_text(render_openapi())


if __name__ == "__main__":
    main()
