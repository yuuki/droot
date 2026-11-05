## What and why

<!-- What does this change, and which issue does it address (Closes #...)? -->

## How it was tested

<!-- Unit tests, `make test-root`, `make integration-test`, manual checks. -->

## Checklist

- [ ] `make lint test` passes (and `make test-root` / `make integration-test`
      for changes to `run`, `umount` or `export`).
- [ ] User-visible changes are described in `CHANGELOG.md` (under "Unreleased")
      and in the README or the documentation.
- [ ] Paths inside the container root are resolved with `osutil.Root`.
- [ ] No new dependency, or `LICENSE_OF_DEPENDENCIES.md` is updated.
