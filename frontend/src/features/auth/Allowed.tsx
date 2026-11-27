import type { ReactNode } from "react";
import { useCan } from "../../api/auth";
import type { Action } from "../../api/types";

/** Shows its children only to users the server would let do `action`. It
 * hides what would fail; it protects nothing, since the api checks every request. */
export function Allowed({ action, children }: { action: Action; children: ReactNode }) {
  return useCan(action) ? children : null;
}
