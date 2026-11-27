## Summary

<!-- What this changes and why. Link the issue: "Fixes #123". For a large change, link the issue where the approach was agreed. -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Change to existing behaviour (describe the effect on users and on stored runs)
- [ ] Refactor, tests or tooling only

## How it was tested

<!-- Commands you ran and what they showed. For behaviour you can see, add the run, output or screenshot. -->

## Checklist

See [CONTRIBUTING.md](https://github.com/servtuner/servtuner/blob/main/CONTRIBUTING.md) for each rule.

- [ ] `just gen-check`, `just lint` and `just test` pass.
- [ ] UI or api run flow changed: `just e2e-smoke` passes.
- [ ] Chart changed: `just e2e-tls-off` or `just e2e-mutual-tls` passes on kind.
- [ ] Schema changed in `schema/json/` (or the api's models), regenerated with `just gen`, and no generated file edited by hand.
- [ ] New or changed connections are tested with and without TLS and client certificates, as they support, with no skip-verify option.
- [ ] No secrets, addresses or hostnames in code, tests, RunSpecs or values; placeholders and `*.localtest.me` only.
- [ ] New dependencies are pinned exactly, with the reason below.
- [ ] Changes the requests the workload generator sends: the workload hash changes for the affected workloads.
- [ ] User-facing change: the documentation is updated, or a pull request in [servtuner/docs](https://github.com/servtuner/docs) is linked.

## Notes for reviewers

<!-- Anything to look at first, trade-offs you made, or follow-ups left out of this change. Delete if empty. -->
