# AGENTS.md

This file holds only what the code does not show. The threat model behind
the rules is [documentation/security.md](documentation/security.md).

## Rules the code does not show

droot runs as root on content that image authors and containers control:

- Resolve every path inside a container root with `osutil.Root`; never
  `filepath.Join(root, p)` followed by an `os.*` call or a mount.
- Never mount on the root itself or inside kernel pseudo file systems
  (`mounter.NotKernelFS`); every mount is a slave mount. `umount` never
  follows symlinks and checks the mount ID before detaching.
- Change capabilities and credentials on the thread that calls `execve(2)`:
  `runtime.LockOSThread` before the first mount, no goroutine hand-off in
  between. Empty the inheritable and ambient sets; create device nodes only
  on the container's own file system.
- `droot export` never runs the image and removes its temporary container
  even after an error or a signal; it streams, never holding the archive in
  memory.
- Credentials go over HTTPS only (plain HTTP only to loopback registries),
  redirects included.
- Rootless, re-execute in the user namespace before reading anything
  (`--config` and `--env-file` may be pipes).
- Shell out with fixed argument vectors, never through a shell, with a
  timeout and `WaitDelay` when the command may hang.
- `cancelableWriter` and `openCancelable` abandon an operation blocked on a
  FIFO on purpose (the process exits right after); do not make them wait.
- Privileged tests never `os.RemoveAll` a directory that may hold a bind
  mount: it would delete host files through it.
- Dependencies: `golang.org/x/sys` only; a new one needs a strong reason
  and an update of `LICENSE_OF_DEPENDENCIES.md`.
- User-visible changes get an entry under "Unreleased" in `CHANGELOG.md`
  (the release notes) and in the README or `documentation/`.
- Keep a fuzz crasher in `testdata/fuzz/FuzzXxx/` with its fix, as a seed.
- Bump pinned tool versions in the `Makefile` and the workflows together.
- English only.

## Compatibility contracts

Call any change out in the pull request and in `CHANGELOG.md` (under
"Breaking changes" if needed):

- Commands, flags, defaults and exit status: `internal/commands`; the long
  flag names of `droot run` are also the `--config` keys (`internal/conffile`).
- Archive format: `/.drootenv` (read by droot 0.7; `internal/environ`) and
  `/.droot/config.json` (`internal/imageconfig`; increment `Version` only
  for incompatible changes).
- Environment variables: `DROOT_*` (`internal/commands`), the Docker ones
  (`internal/docker`), the systemd ones passed on to `COMMAND`
  (`internal/systemd`).
- The lock directory (`internal/osutil/lock_unix.go`) and the label of
  temporary export containers (`internal/docker/export.go`).
- Release asset names and layout (download scripts depend on them):
  `.goreleaser.yaml`.

## Before opening a pull request

- `make check`.
- `run`, `umount`, `unshare`, `mounter`, `osutil`, `identity`, `userns`:
  also `make test-root`.
- `run`, `umount`, `export`, the Docker or registry client, the archive
  format: also `make integration-test`.
- A parser: also `make fuzz FUZZTIME=1m` for its targets.
- Flags or help texts: `make docs`, and commit the result.

## Git conventions

Branches: `<type>/<issue>-<summary>`, or `<type>/<summary>` without an issue
(`fix/166-connect-errors`, `feat/qp-counters`). `<type>` is a commit type
below; `<summary>` is 2–5 lowercase ASCII words joined by `-`. Branches named
by tools keep their names (`agent/*`, `claude/*`, `copilot/*`,
`dependabot/*`). Never push to the default branch.

Commits follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

    <type>(<scope>)!: <subject>

    <body>

    <footer>

- `type`: `feat` (new user-visible behaviour), `fix` (bug fix), `perf`,
  `refactor` (no behaviour change), `test`, `docs`, `build` (build system,
  dependencies, packaging), `ci` (workflows), `chore` (anything else, e.g.
  `chore(release): prepare v1.2.3`), `revert`.
- `scope`: optional; the package or area changed: `run`, `export`,
  `umount`, `osutil`, `mounter`, `docker`, `image`, `userns`.
- `!` and a `BREAKING CHANGE: <what changed and how to migrate>` footer for
  any change to a compatibility contract above.
- `subject`: imperative, lower case (except identifiers), no final period,
  at most 72 characters including the prefix.
- `body`: why the change is needed and what it affects, wrapped at 72
  columns; required unless the subject says everything.
- `footer`: `Fixes #N` / `Refs #N`, `BREAKING CHANGE:`, trailers such as
  `Co-Authored-By:`.
- One logical change per commit; regenerated files go in the commit that
  changes their source. Pull request titles use the same format (they become
  the squash commit).

## Forbidden

- Disabling, skipping, deleting or weakening tests, fuzz targets or lint
  rules to get green (`t.Skip` without a real precondition, `|| true`,
  `continue-on-error`, new blanket exclusions instead of a
  `//nolint:<linter> // <reason>`).
- Editing `documentation/cli.md` or `documentation/droot.1` by hand.
- Adding dependencies without a justification in the pull request.
- Changing a compatibility contract without calling it out.
- Loosening workflow permissions, unpinning actions, adding
  `pull_request_target`.
- Committing secrets, rewriting published history, force-pushing, moving
  tags or publishing releases.

## Needs human review

Even with green CI: `.github/workflows/`, `.goreleaser.yaml`, the release
scripts in `script/`, permissions and secrets; `osutil`, `mounter`,
`userns`, `seccomp`, `identity`, `internal/commands/run.go`; the parsers of
untrusted input (`docker` tar rewriting, `image` flattening and registry
client, `environ`, `imageconfig`, `conffile`); the compatibility contracts,
new dependencies and large refactors.

## Environment notes

- With `GH_TOKEN` set but the GitHub API unreachable, run
  `env -u GH_TOKEN make check`.
