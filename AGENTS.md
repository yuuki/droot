# AGENTS.md

Guide for coding agents (and humans) working on droot. The user-facing
documentation is the [README](README.md) and [documentation/](documentation);
this file is the short, verified list of how to build, test and change the
code safely. Keep it in sync when a command, a contract or an invariant
changes.

## 1. Overview and architecture

droot is a chroot-based application container engine, written in Go: build
an image with `docker build`, ship a plain tar archive, run it with
`chroot(2)`. It deliberately uses no namespaces by default (no PID, network,
user or cgroup isolation) and depends on one module, `golang.org/x/sys`.

- `droot export` writes the file system of an image as a tar stream, from a
  Docker Engine API daemon (spoken to with `net/http`, no SDK) or without
  daemon (registry, `docker save` archive, OCI layout: `internal/image`
  flattens the layers itself). The stream is rewritten on the fly to add
  `/.drootenv` and `/.droot/config.json`.
- `droot run` runs as root (or rootless in a user namespace): it resolves
  paths inside the container root race-free, mounts `/proc`, `/sys` and bind
  mounts, populates `/dev`, drops capabilities, sets the credentials, then
  `chroot(2)`s and `execve(2)`s the command (no droot process remains).
- `droot umount` detaches the mounts below a root; `droot status` and
  `droot unshare` complete the set.

Read [documentation/architecture.md](documentation/architecture.md) for the
step-by-step data flow of each command, and
[documentation/security.md](documentation/security.md) for the threat model.

## 2. Directory layout

| Path | Content |
|---|---|
| `cmd/droot` | `main`: signal handling, version stamping |
| `internal/cli` | Command line parser, help, completions |
| `internal/commands` | The commands (`export`, `run`, `umount`, `status`, `unshare`) and their flags |
| `internal/docker` | Minimal Docker Engine API client, credentials, export and tar rewriting |
| `internal/image` | Registry client, `docker save`/OCI readers, layer flattening |
| `internal/osutil` | `osutil.Root` (race-free path resolution), devices, capabilities, credentials, exec |
| `internal/mounter` | Mounts, `/proc/self/mountinfo` parsing, unmounting |
| `internal/identity` | `--user`/`--group` resolution (container's `/etc/passwd` and `/etc/group`, then the host) |
| `internal/userns` | Rootless mode: ID mappings, re-execution in a user namespace, supervision |
| `internal/environ`, `internal/imageconfig`, `internal/conffile`, `internal/seccomp`, `internal/systemd`, `internal/log` | `/.drootenv` and env files, `/.droot/config.json`, `--config` files, the seccomp filter, systemd variables, logging |
| `internal/cmd/gendocs` | Generates `documentation/cli.md` and `documentation/droot.1` |
| `documentation/` | User documentation; `cli.md` and `droot.1` are generated |
| `examples/` | systemd units, dewy hook, a `--config` file |
| `script/` | `integration-test.sh` (end-to-end), release scripts and their test |
| `.github/` | CI, release, CodeQL and complexity workflows, Dependabot, templates |

## 3. Build

```console
$ make build      # static ./droot (CGO_ENABLED=0), version from git describe
$ make cross      # build for every released platform
$ make snapshot   # release assets into dist/ without publishing (GoReleaser)
```

Go: the `go` line of `go.mod` (1.26) or later; CI also tests the latest
release. With the default `GOTOOLCHAIN=auto`, the go command downloads the
toolchains the pinned tools need.

## 4. Test

| Command | What | Needs |
|---|---|---|
| `make test` (= `make test-race`) | Unit tests with `-race`; root-only tests skip | nothing (Docker is faked or served by `httptest`) |
| `make test-root` | Unit tests as root, including real mounts, `mknod`, capabilities | root on Linux (uses `sudo` unless root) |
| `make fuzz` (= `make test-fuzz`) | Every `FuzzXxx` for `FUZZTIME` (30s) each; seeds and `testdata/fuzz` also run with `make test` | nothing |
| `make integration-test` (= `make test-integration`) | `script/integration-test.sh`: end-to-end against Docker (or Podman) | root, Docker, network to pull `alpine`, `busybox`, `registry:2` |
| `make test-scripts` | Release scripts | bash |
| `make bench` | Benchmarks (`BENCH_COUNT=6`) into `bench.txt`; compare with `benchstat` | nothing |
| `make cover` | Coverage profile in `coverage.out` | nothing (run as root for the mount tests) |

Fuzz targets cover the parsers of untrusted input: `/.drootenv`
(`environ`), `/proc/self/mountinfo` (`mounter`), path resolution of
`osutil.Root` (Linux), the rewriting of the exported archive (`docker`), the
flattening of layers and the registry's `WWW-Authenticate` header and image
references (`image`), `--config` files (`conffile`), and the image's
`/etc/passwd`/`/etc/group` (`identity`, Linux). A fuzz failure leaves the
input in `testdata/fuzz/FuzzXxx/`: keep it as a regression seed with the fix.

Privileged tests create scratch directories with `os.MkdirTemp` and delete
them only when nothing is mounted below: never `os.RemoveAll` a directory
that may hold a bind mount, it would delete host files through it.

## 5. Static analysis

```console
$ make lint          # golangci-lint (.golangci.yml) and go mod tidy -diff
$ make fmt           # gofmt + goimports (local prefix github.com/yuuki/droot)
$ make vet           # go vet for Linux and macOS (GOOS=darwin)
$ make lint-actions  # actionlint and zizmor on .github/ (offline without GH_TOKEN)
$ make vuln          # govulncheck
```

Tool versions are pinned in the `Makefile` and the workflows (golangci-lint
v2.15.0, govulncheck v1.9.0, actionlint v1.7.12, zizmor 1.32.0, GoReleaser
v2.20.0, syft, cccc); bump them together. The `gosec` exclusions in
`.golangci.yml` each carry a reason; add a `//nolint:<linter> // <reason>`
for a single justified exception rather than a new exclusion. CodeQL
(`.github/workflows/security.yml`) and the Code Complexity workflow
(`cccc.toml`: a Cognitive Complexity over 30 fails) run on GitHub.

## 6. Coding conventions

- Errors: wrap with `%w` and context (`fmt.Errorf("read %s: %w", ...)`),
  messages in lowercase, naming the file or the flag concerned; use
  `fs.PathError` for path operations.
- Logging: `internal/log` only (`Debugf`, `Infof`, `Warnf`, to STDERR,
  text or JSON with `DROOT_LOG_FORMAT`); never the standard `log` package.
- Comments and documentation in English; comments say why. Exported
  identifiers have doc comments (revive).
- No new dependency without a strong reason; update
  `LICENSE_OF_DEPENDENCIES.md` if one is added. Shell out (`zstd`, `ssh`,
  `getent`, credential helpers, `newuidmap`) with fixed argument vectors,
  never through a shell, and with a timeout (and `WaitDelay`) when the
  command may hang.
- Commits: short subjects scoped by package or area (`run: ...`,
  `export: ...`, `osutil: ...`, `ci: ...`, `docs: ...`, `release: ...`), the
  reason in the body.
- User-visible changes get an entry under "Unreleased" in `CHANGELOG.md`
  (the release notes are taken from it) and in the README or
  `documentation/`.

## 7. Compatibility contracts

Do not change these silently; call them out in the pull request and in
`CHANGELOG.md` (with a "Breaking changes" section if needed):

- Commands, flags, their defaults and exit status: `internal/commands`,
  documented in `documentation/cli.md` and `droot.1` (generated by
  `make docs`; a unit test fails when they are stale). The long flag names
  of `droot run` are also the keys of `--config` files (`internal/conffile`),
  with `command`.
- The archive format: `/.drootenv` (one `KEY=VALUE` per line, read by droot
  0.7) and `/.droot/config.json` (`imageconfig.Version`, incremented only
  for incompatible changes; unknown fields are ignored).
- Environment variables: `DROOT_DEBUG`, `DROOT_QUIET`, `DROOT_LOG_FORMAT`,
  the Docker ones (`DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_API_VERSION`,
  `DOCKER_CONFIG`, ...), and the systemd ones passed on to `COMMAND`.
- Runtime paths and labels: the lock `/run/droot/DEV-INODE.lock` (rootless:
  `$XDG_RUNTIME_DIR/droot/`), the label `io.github.yuuki.droot` of temporary
  export containers.
- Release assets: names and layout of the archives, binaries, deb/rpm
  packages and checksums (download scripts depend on them; see
  [documentation/development.md#assets](documentation/development.md#assets)).

## 8. Security invariants, concurrency and resources

droot runs as root on content that the image's author, and processes of a
running container, control. These invariants are what keep it from being
turned against the host:

- **Every path inside a container root is resolved with `osutil.Root`**
  (`OpenFile`, `OpenDir`, `OpenMountTarget`, `OpenNoFollow`, `MknodChar`,
  `CopyFileIn`, ...): component by component with `openat(2)` and
  `O_NOFOLLOW`, symlinks followed by droot as if the root were `/`, and the
  final operation done on the resolved descriptor (`mknodat`, `renameat`,
  mounting on `/proc/self/fd/N`). Never `filepath.Join(root, p)` followed by
  an `os.*` call or a mount on the result.
- Mount targets are never the root itself nor inside kernel pseudo file
  systems (`mounter.NotKernelFS`); every mount is a slave mount; `umount`
  never follows symlinks and checks the mount ID before detaching.
- Capabilities and credentials change on the OS thread that calls
  `execve(2)`: `runtime.LockOSThread` before the first mount, and no
  goroutine hand-off in between. The inheritable and ambient sets are
  emptied; device nodes are created only on the container's own file system.
- `droot export` never runs the image; the temporary container is removed
  even after an error or a signal. Credentials go over HTTPS only (plain
  HTTP to loopback registries), and redirects keep that rule.
- Rootless, droot re-executes itself in the user namespace before reading
  anything (`--config` and `--env-file` may be pipes).

Concurrency: droot is a short-lived command. Goroutines are bounded to one
operation: the producer of `docker.WriteArchive` (joined before return),
the progress ticker (stopped by `end`), the stderr copier of an `ssh`
connection, the signal forwarder of rootless mode, and `cancelableWriter`
and `openCancelable`, which deliberately abandon a write or an `open(2)`
blocked on a FIFO when the context is cancelled (the process exits right
after). `cmd/droot` cancels the context on SIGINT, SIGTERM and SIGHUP.
Close every descriptor (`defer f.Close()`); `osutil.Root` methods return
descriptors the caller owns.

## 9. Performance

`droot export` streams: the archive is never held in memory (a 1 MiB
buffered writer, `io.Pipe` between producer and rewriter); only the image's
own `/.drootenv` and `/.droot` files are buffered (64 MiB at most). Layers are
downloaded three at a time to `$TMPDIR` and flattened in two passes that
read the headers first. Path resolution costs one `openat(2)` per
component. `make bench` measures the tar rewriting, the flattening and
`osutil.Root` resolution.

## 10. Before opening a pull request

1. `make check` (lint, `go mod tidy -diff`, actionlint and zizmor, vet for
   Linux and macOS, the release scripts' test, the unit tests with `-race`).
2. When touching `run`, `umount`, `unshare`, `mounter`, `osutil`,
   `identity` or `userns`: `make test-root`.
3. When touching `run`, `umount` or `export` (or the Docker client, the
   registry client, the archive format): `make integration-test`.
4. When touching a parser: `make fuzz FUZZTIME=1m` for its targets.
5. When changing flags or help texts: `make docs` and commit the result.
6. `CHANGELOG.md` and the documentation updated for user-visible changes.

## 11. Rules for AI agents

Forbidden:

- Disabling, skipping, deleting or weakening tests, fuzz targets or lint
  rules to get a green build (`t.Skip` without a real precondition,
  `|| true`, `continue-on-error`, new blanket exclusions).
- Editing generated files by hand: `documentation/cli.md` and
  `documentation/droot.1` come from `make docs`.
- Adding dependencies without a justification in the pull request.
- Changing the contracts of section 7 without calling it out.
- Loosening workflow permissions, unpinning actions (every `uses:` is a
  full commit SHA with its version in a comment), or adding
  `pull_request_target`.
- Committing secrets, rewriting published history, force-pushing, moving
  tags or publishing releases.

## 12. Changes that need human review

Even when CI is green, ask a maintainer to review:

- `.github/workflows/`, `.goreleaser.yaml`, `script/` release scripts,
  permissions and secrets.
- Security-sensitive code: `osutil` (path resolution, capabilities,
  credentials), `mounter`, `userns`, `seccomp`, `identity`, `commands/run.go`,
  and the parsers of untrusted input (`docker` tar rewriting, `image`
  flattening and registry client, `environ`, `imageconfig`, `conffile`).
- The contracts of section 7, new dependencies, and large refactors.
