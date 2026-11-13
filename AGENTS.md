# AGENTS.md

## Rules
- droot runs as root on content image authors and containers control; the threat model is `documentation/security.md`.
- Resolve every path inside a container root with `osutil.Root`; never `filepath.Join(root, p)` followed by an `os.*` call or a mount.
- Never mount on the root itself or inside kernel pseudo file systems (`mounter.NotKernelFS`); every mount is a slave mount.
- `umount` never follows symlinks and checks the mount ID before detaching.
- Change capabilities and credentials on the thread that calls `execve(2)`: `runtime.LockOSThread` before the first mount, no goroutine hand-off.
- Empty the inheritable and ambient capability sets; create device nodes only on the container's own file system.
- `droot unit` writes image content into units that systemd reads as root: every value goes through `internal/systemd/unitfile.go` (quoted, control characters refused); content never chooses a setting.
- `droot export` never runs the image, removes its temporary container even after an error or a signal, and streams (never holds the archive in memory).
- Credentials go over HTTPS only, redirects included; plain HTTP only to loopback registries.
- Rootless, droot re-executes itself in the user namespace before reading anything (`--config` and `--env-file` may be pipes).
- `cancelableWriter` and `openCancelable` abandon an operation blocked on a FIFO on purpose (the process exits right after); do not make them wait.
- Privileged tests never `os.RemoveAll` a directory that may hold a bind mount: it would delete host files through it.
- Dependencies: `golang.org/x/sys` only; a new one needs a strong reason and an update of `LICENSE_OF_DEPENDENCIES.md`.

## Compatibility contracts
- Commands, flags, defaults and exit status (`internal/commands`); the long flag names of `droot run` are also the `--config` keys (`internal/conffile`).
- Archive format: `/.drootenv` (read by droot 0.7; `internal/environ`), `/.droot/config.json` (`internal/imageconfig`, `Version` bumped only if incompatible).
- Environment variables (`DROOT_*`, Docker's, systemd's passed to `COMMAND`); lock directory (`internal/osutil/lock_unix.go`); export label (`internal/docker/export.go`).
- Release asset names and layout, which download scripts depend on: `.goreleaser.yaml`.

## Before opening a pull request
- `make check`; also `run`, `umount`, `unshare`, `mounter`, `osutil`, `identity`, `userns` → `make test-root`; a parser or the unit writer → `make fuzz FUZZTIME=1m`.
- `run`, `unit`, `umount`, `export`, the Docker or registry client, the archive format → `make integration-test` (`unit` needs systemd as init); flags or help texts → `make docs`.

## Git conventions
- Branches: `<type>/<issue>-<summary>` (`<type>/<summary>` without an issue); tools' `agent/*`, `claude/*`, `copilot/*`, `dependabot/*` keep their names.
- Commits and PR titles: Conventional Commits 1.0.0, types `feat` `fix` `perf` `refactor` `test` `docs` `build` `ci` `chore` `revert`; scopes: `run`, `unit`, `export`, `umount`, `osutil`, `mounter`, `docker`, `image`, `userns`.
