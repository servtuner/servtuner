import { ApiError } from "../../api/client";

/** What to tell a person about a failed auth call: the server's message, else the error. */
export function authErrorText(error: Error): string {
  return error instanceof ApiError && error.detail ? error.detail : error.message;
}
