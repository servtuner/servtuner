import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterAll, afterEach, beforeAll } from "vitest";
import { server } from "./server";

// Mantine needs these browser APIs, which jsdom lacks.
window.matchMedia ??= (query: string) =>
  ({
    matches: false,
    media: query,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }) as unknown as MediaQueryList;
Element.prototype.scrollIntoView ??= function scrollIntoView() {};
// Mantine's autosizing Textarea listens for web fonts loading; jsdom has no FontFaceSet.
if (!("fonts" in document)) {
  Object.defineProperty(document, "fonts", {
    value: { addEventListener: () => undefined, removeEventListener: () => undefined },
  });
}
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// Lazy pages (Compare, Experiment: ECharts) load a chunk on first render. With every test file running
// in parallel that can take longer than Testing Library's default 1 s, which made findBy* flaky.
const ASYNC_UTIL_TIMEOUT_MS = 5000;
configure({ asyncUtilTimeout: ASYNC_UTIL_TIMEOUT_MS });

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => {
  cleanup();
  server.resetHandlers();
});
afterAll(() => server.close());
