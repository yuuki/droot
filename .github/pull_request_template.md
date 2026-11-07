## What and why

<!-- What does this change, and which issue does it address (Closes #...)? -->

## How it was tested

<!-- Unit tests, `make test-root`, `make integration-test`, fuzzing, manual checks. -->

## Checklist

- [ ] `make check` passes (and `make test-root` / `make integration-test`
      for changes to `run`, `umount` or `export`).
- [ ] Behaviour changes come with tests.
- [ ] User-visible changes are described in `CHANGELOG.md` (under "Unreleased")
      and in the README or the documentation.
- [ ] The compatibility contracts (flags, archive format, release assets,
      see `AGENTS.md`) are unchanged, or the change is called out above.
- [ ] Paths inside the container root are resolved with `osutil.Root`.
- [ ] No new dependency, or `LICENSE_OF_DEPENDENCIES.md` is updated.
- [ ] Changes written with an AI assistant were reviewed by a human.
