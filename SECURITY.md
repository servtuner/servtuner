# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/servtuner/servtuner/security/advisories/new),
not in a public issue or pull request.

Include what you found, how to reproduce it, and the version or commit you
tested. You will get an acknowledgement, and a fix or a decision, as soon as
the report has been triaged.

## Supported versions

Fixes go into the latest release. Older releases are not patched.

## Scope

servtuner handles benchmark targets' credentials and runs load against
endpoints its users name, so these matter most:

- a credential reaching a run bundle, a log, an API response or the UI;
- a way around authentication or a role's limits in the api;
- a connection made in a weaker transport mode than the one configured
  (`mtls`, `tls` or `platform`), or certificate verification skipped;
- a run or a managed target reaching namespaces or hosts it was not given.
