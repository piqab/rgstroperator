# Contributing to rgstroperator

Thanks for your interest! Bug reports, ideas and pull requests are welcome,
in English or Russian.

- **Bugs and features:** open an [issue](https://github.com/piqab/rgstroperator/issues/new/choose).
  For larger changes, please open an issue first to agree on the approach.
- **Security problems:** see [SECURITY.md](SECURITY.md), never a public issue.

## Development

Requirements: Go 1.26+, make, Docker (kind and helm are installed into
`./bin` by make).

```bash
make test                     # unit + envtest
make generate bundle          # after changing api/ or RBAC markers
RGSTR_SRC=../rgstr make e2e   # full test in kind
```

The CRD, RBAC, deepcopy code, `deploy/operator.yaml` and the chart are
generated; CI fails if they are not committed up to date.

## Pull requests

- Keep a pull request to one change, with tests for new behaviour.
- Match the surrounding code style; run `gofmt`.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat: ...`, `fix: ...`, `docs: ...`.
- Do not change `VERSION`: maintainers set it when releasing.

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE).
