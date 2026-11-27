// The one place the context path is derived. The deployed image serves
// index.html with a <base href> chosen per request by nginx (frontend/nginx.conf.template),
// so every app URL derives from document.baseURI and none is an absolute path
// (frontend/CLAUDE.md, "Context path").

/** The router basename for a base URI: its pathname without the trailing slash ("/" at the root). */
export function routerBasename(baseUri: string = document.baseURI): string {
  const pathname = new URL(baseUri).pathname.replace(/\/+$/, "");
  return pathname === "" ? "/" : pathname;
}

/** Resolve a URL that may be relative (to the base URI) or absolute. */
export function resolveAgainstBase(url: string, baseUri: string = document.baseURI): string {
  return new URL(url, baseUri).toString();
}
