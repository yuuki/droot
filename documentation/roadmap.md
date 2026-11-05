# Known issues and ideas

This page lists the known limitations of droot and ideas for its future,
collected during the 2026 maintenance. Items fixed in that maintenance are
listed in the [changelog](../CHANGELOG.md). Contributions are welcome; please
open an issue before starting on a large item.

Legend: **[security]**, **[bug]**, **[compat]** (compatibility),
**[ux]**, **[feature]**, **[project]**.

## Known issues and limitations

### `droot run`

- **[security]** Running as root inside droot is close to running as root on
  the host: `/proc` and `/sys` are the host's and writable (`/proc/sys/*`,
  `/proc/sysrq-trigger`, sysfs attributes), and root keeps
  `CAP_DAC_OVERRIDE`, `CAP_CHOWN` and `CAP_FOWNER`. Ideas: mount sysfs
  read-only, mask sensitive `/proc` paths like Docker does
  (`/proc/kcore`, `/proc/sysrq-trigger`, `/proc/sys` read-only, ...), or
  offer a PID namespace.
- **[security]** No seccomp filter or LSM profile, and `no_new_privs` is
  opt-in (`--no-new-privileges`): by default setuid-root binaries in the image
  regain the capabilities left in the bounding set.
- **[bug]** Mounts are made in the host's mount namespace and persist after
  the command exits; a failure halfway leaves the mounts already made. Users
  must remember `droot umount`, and concurrent runs share mounts.
- **[compat]** Only `ENV` of the image is used. `WORKDIR`, `USER`,
  `ENTRYPOINT`/`CMD`, `STOPSIGNAL` are ignored: the working directory is
  always `/` and the command must always be given.
- **[compat]** `HOME`, `USER`, `LOGNAME` and `HOSTNAME` are not set, and the
  environment is empty when there is neither `/.drootenv` nor `--env`
  (`PATH` lookup falls back to a default path, but the variable is not set).
- **[compat]** Only `/dev/null`, `/dev/zero`, `/dev/random` and `/dev/urandom`
  are created. `/dev/pts`, `/dev/ptmx`, `/dev/tty`, `/dev/full`, `/dev/shm`
  (tmpfs, needed e.g. by Python multiprocessing), `/dev/fd` and
  `/dev/std{in,out,err}` are missing, and `/tmp`/`/run` are not tmpfs.
- **[ux]** Inherited behavior whose purpose is unclear: `--bind` creates a
  `.droot.keep` file in empty host directories. Decide whether to keep it.
- **[compat]** `--cp` copies snapshots of the host files: later changes to the
  host's `/etc/resolv.conf` or `/etc/hosts` are not reflected (a bind mount
  would be live).
- **[compat]** `droot run` needs real root; rootless operation with user
  namespaces is not supported.
- **[ux]** Two concurrent `droot run` on the same root race on `--cp` copies
  and device creation (harmless in practice, but not guaranteed).

### `droot export`

- **[compat]** A Docker Engine API daemon and a local image are required:
  there is no pull (and no registry authentication), no support for
  `docker save`/OCI layouts, and `--platform` only selects among the variants
  present locally.
- **[compat]** `ssh://` and `npipe://` `DOCKER_HOST`s are not supported.
- **[compat]** Environment variables with multi-line values or keys starting
  with `#` cannot be represented in `/.drootenv` and are skipped (with a
  warning). The file has no version marker or escaping.
- **[ux]** No progress indication for large images; no compression option
  (pipe to `gzip`/`zstd`).
- **[security]** Archives are neither checksummed nor signed by droot.
- **[compat]** The archive contains the files Docker adds to every container
  (`/.dockerenv`, empty `/etc/hostname`, `/etc/hosts`, `/etc/resolv.conf`,
  `/dev/console`, ...).

### Project

- **[project]** The Homebrew tap (`yuuki/homebrew-droot`) is not updated by
  the release workflow and must be checked against the new asset names.
- **[project]** Windows is not supported at all.

## Feature ideas

### Running

- **[feature]** `--unshare` (opt-in): a private mount namespace so that mounts
  disappear with the process, optionally PID/UTS/IPC namespaces, keeping the
  default as simple as today.
- **[feature]** Save the image config (`Entrypoint`, `Cmd`, `WorkingDir`,
  `User`, `StopSignal`) as `/.droot/config.json` at export time and use it as
  defaults in `droot run` (so `droot run --root DIR` alone works).
- **[feature]** `--workdir`/`-w`, `--env-file`, `--env KEY` (take the value from
  droot's environment), automatic `HOME`/`USER`.
- **[feature]** `--no-new-privileges`, `--cap-add`/`--cap-drop`, an optional
  seccomp profile, `--read-only` root.
- **[feature]** `--tmpfs PATH`, `/dev/shm`, `/dev/pts` and the other common
  devices and symlinks.
- **[feature]** Docker-style bind options: `--bind SRC:DEST:ro`, recursive binds,
  propagation modes.
- **[feature]** Live `/etc/resolv.conf`/`/etc/hosts` via bind mounts as an
  alternative to `--cp`.
- **[feature]** Resource limits: `--ulimit`, cgroup v2 (`--memory`, `--cpus`,
  `--pids-limit`), or documented `systemd-run --scope` integration.
- **[feature]** `droot status --root DIR` (mounts and processes using a root)
  and `droot umount --all`.
- **[feature]** Roll back the mounts made by a failed `droot run`.
- **[feature]** Rootless mode with user namespaces.

### Building and shipping

- **[feature]** `droot export --pull` with registry authentication from
  `~/.docker/config.json` and credential helpers.
- **[feature]** Daemonless export: flatten images straight from a registry, a
  `docker save` tarball or an OCI layout (works on CI runners without Docker
  and with Podman/Buildah/containerd stores).
- **[feature]** `--compress gzip|zstd`, checksum files, reproducible archives
  (normalized mtimes, sorted entries) and `--exclude` patterns to slim images.
- **[feature]** `droot deploy`/`droot import`: download (S3/HTTP), verify a
  checksum or signature (cosign, minisign), extract atomically and switch a
  symlink while keeping N releases (capze-like), then optionally restart a
  systemd unit.
- **[feature]** `ssh://` daemons through `ssh HOST docker system dial-stdio`.

### Usability

- **[ux]** man pages and Markdown CLI reference generated from the CLI
  definition (`internal/cli`).
- **[ux]** `droot version` with commit and build date; `--quiet`; structured
  logs (`log/slog`, JSON).
- **[ux]** A declarative file (e.g. `/etc/droot/app.toml`) describing the run
  options of an application, usable from systemd units.
- **[ux]** Example systemd unit files and a deployment tutorial.

### Project and maintenance

- **[project]** Automate the Homebrew tap with GoReleaser, or drop it.
- **[project]** Sign releases (cosign keyless), publish an SBOM and build
  provenance (GitHub artifact attestations).
- **[project]** Fuzz tests for `environ.ParseEnv`, `mounter.ParseMountInfo`,
  the path resolution of `osutil.Root` and the tar rewriting.
- **[project]** End-to-end tests on arm64 runners and with Podman; coverage
  reporting.
- **[project]** `SECURITY.md`, issue/PR templates, a Japanese README.
