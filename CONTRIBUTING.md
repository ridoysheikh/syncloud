# Contributing to SynCloud

Thanks for helping! Bug reports, documentation fixes and code are all welcome.

## Reporting a bug

[Open an issue](https://github.com/ridoysheikh/syncloud/issues/new/choose) with:

- the SynCloud version (**Settings → Updates**, or `syncloud-controller version`);
- what you did, what you expected, and what happened;
- relevant logs (`journalctl -u syncloud-controller`, `synctl logs …`), with secrets removed.

**Security problems** don't go in public issues. See [SECURITY.md](SECURITY.md).

## Proposing a change

For anything bigger than a fix, open an issue first to agree on the approach. The design and the reasoning behind it are in [plan/PLAN.md](plan/PLAN.md). New features are planned there before they're built.

## Making a change

1. Build and run it locally ([Building from source](docs/development/building.md)): `make dev`.
2. Keep to the existing style. Run `make fmt`. Code comments explain *why*, not *what*.
3. Add tests:
   - unit tests next to the code;
   - an end-to-end test in `test/e2e/` when the change affects what runs on nodes ([Testing](docs/development/testing.md)).
4. Update the documentation in `docs/` when behavior changes, and add a line to `CHANGELOG.md` under **Unreleased**.
5. Make sure `make vet test` passes, then open a pull request that says what changed and why.

Each API operation needs a matching synctl command, and each route must be in the OpenAPI spec. Tests enforce both.

## Dashboard conventions

- Dark theme only.
- Text contrast of at least 8.5:1, borders that blend into the background, and no gradients.
- Custom dialogs, never `confirm()`/`alert()`.
- Cards, chips, segmented controls and toggles instead of bare checkboxes and radios.
- Tasks, nodes and services are drawn by the shared components in `web/src/entities`.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
