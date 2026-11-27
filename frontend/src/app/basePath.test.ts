import { describe, expect, it } from "vitest";
import { resolveAgainstBase, routerBasename } from "./basePath";

describe("routerBasename", () => {
  it.each([
    ["http://host/", "/"],
    ["http://host/servtuner/", "/servtuner"],
    ["http://host/a/b/c/", "/a/b/c"],
    ["http://host/a/b/c", "/a/b/c"],
  ])("%s gives %s", (baseUri, expected) => {
    expect(routerBasename(baseUri)).toBe(expected);
  });
});

describe("resolveAgainstBase", () => {
  it.each([
    ["config.json", "http://host/", "http://host/config.json"],
    ["config.json", "http://host/servtuner/", "http://host/servtuner/config.json"],
    ["api", "http://host/a/b/c/", "http://host/a/b/c/api"],
    ["https://api.example.test/v1", "http://host/a/", "https://api.example.test/v1"],
    ["/api", "http://host/a/", "http://host/api"],
  ])("%s against %s gives %s", (url, baseUri, expected) => {
    expect(resolveAgainstBase(url, baseUri)).toBe(expected);
  });
});
