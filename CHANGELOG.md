# Changelog

All notable changes to this project are documented in this file. The format
is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Releases up to 0.7.7 are described on the
[GitHub releases page](https://github.com/yuuki/droot/releases).

## [Unreleased]

### Added

- `droot run` works with `Type=notify` and socket activation under systemd
  ([#52](https://github.com/yuuki/droot/issues/52)): the variables systemd
  sets for droot are passed on to `COMMAND` without `--env`
  (`NOTIFY_SOCKET`; `LISTEN_FDS`, `LISTEN_PID`, `LISTEN_FDNAMES` and
  `LISTEN_PIDFDID` when `LISTEN_PID` is droot's PID; `WATCHDOG_USEC` and
  `WATCHDOG_PID` when `WATCHDOG_PID` is droot's PID or unset), between the
  image's environment and `--env-file`. A `NOTIFY_SOCKET` that is a path
  is bind mounted read-only on the same path in the container, unless a
  `--bind` or `--tmpfs` mounts on that path or the socket is already
  there; one that cannot be bound only gets a warning.
- `examples/systemd/app-notify.service` (`Type=notify`) and
  `examples/systemd/app.socket`, described in the deployment guide.

### Fixed

- Rootless, the sockets of socket activation reach `COMMAND`: the
  supervising droot passed a pipe of its own as descriptor 3, which
  replaced the first socket. `LISTEN_PID` and `WATCHDOG_PID` are now the
  PID of `COMMAND` instead of that of the supervising droot; its
  notifications need `NotifyAccess=all`, since the supervising droot is the
  main process of the service.
- The mount points `droot run` creates have mode 0755 (directories) and
  0644 (empty files for file bind mounts) whatever the umask: under a unit
  with `UMask=0077`, they were 0700 and 0600, and other users than root
  could not reach what was mounted below them.
- Rootless, the other descriptors droot inherited reach `COMMAND` too, at
  their numbers: the pipe of the supervising droot took descriptor 3 (or the
  one after the sockets of socket activation), so the listening socket of
  server-starter or dewy (`SERVER_STARTER_PORT=8080=3`) was lost. The pipe
  now keeps a number of its own.
- `droot export` of an image without a daemon failed with "not a directory"
  on the paths below a symlink that a later entry of the layer hard-linked
  to itself (a hard link to a symlink is a symlink).
- The 30-second timeout of a Docker credential helper (`credsStore`,
  `credHelpers`) held only if the helper was a single process: when it is a
  script whose child hangs (e.g. on a locked keychain) or outlives it,
  `droot export` waited for that child to close its output, possibly
  forever. droot now stops reading a second after the helper exits or is
  killed.

### Development

- `make check`, the checks to run before a pull request (lint,
  `go mod tidy -diff`, actionlint and zizmor with `make lint-actions`, vet,
  the release scripts' test and the unit tests), `make bench` with
  benchmarks of the archive rewriting, the flattening of layers and the
  path resolution, and the aliases `test-race`, `test-fuzz` and
  `test-integration`.
- Fuzz targets for the image's `/etc/passwd` and `/etc/group`, the
  `WWW-Authenticate` header of registries and image references.
- CodeQL analysis (`.github/workflows/security.yml`), a weekly run of CI
  (longer fuzzing, new advisories), timeouts on every CI job, `AGENTS.md`
  for coding agents and `CONTRIBUTING.md`.
- GitHub Actions: the actions the workflows use, transitive ones included,
  are pinned in the workflow dependency lockfile
  `.github/workflows/actions.lock`, and the workflows name them by release
  tag; `make lint-actions` checks the lockfile (`gh actions-lock --verify` in
  CI) and `make lock-actions` updates it after a `uses:` line changed.

### Documentation

- Deploying with [dewy](https://github.com/linyows/dewy): the artifact
  carries the droot archive, extracted by the hook
  `examples/dewy/droot-extract-release`, and `droot run --unshare --env
  SERVER_STARTER_PORT` runs the application on the listening socket of dewy
  (`examples/dewy/app.service`, `documentation/deployment.md`).

## [0.8.0] - 2026-11-06

Maintenance release after several years: modern Go and Docker, far fewer
dependencies, many bug and security fixes.

### Breaking changes

- Go 1.26 or later is required to build. Release binaries are statically
  linked (`CGO_ENABLED=0`), so droot cannot load NSS modules itself: host
  users and groups that exist only through NSS (LDAP, SSSD, ...) are now
  resolved by running `getent(1)` (looked for in `/usr/bin`, `/bin`,
  `/usr/sbin` and `/sbin`, not in `$PATH`) when they are found neither in the
  container nor in the host's `/etc/passwd` and `/etc/group`.
- `--user NAME` is looked up in the container's `/etc/passwd` first and on the
  host second (previously only on the host). When `--group` is omitted, the
  user's primary group is used instead of droot's own group (`root` under
  sudo), and the supplementary groups are set from the container's
  `/etc/group` instead of being inherited from the invoking root user.
- A numeric `--user` that exists neither in the container nor on the host is
  accepted (droot 0.7 failed with "unknown userid"). Without `--group` it runs
  with droot's own group, usually 0 under sudo, like Docker; a warning is
  printed.
- `CAP_DAC_READ_SEARCH` is no longer kept in the capability bounding set
  (container escape through `open_by_handle_at(2)`).
- Files copied by `--cp` keep the host's ownership and permissions instead of
  being chowned to `--user`/`--group` (which let the application rewrite
  `/etc/passwd` in the container).
- `droot umount` no longer unmounts `ROOT_DIR` itself when it is a mount
  point (e.g. a dedicated file system), only the mounts below it.
- Values read from `/.drootenv` keep their leading and trailing whitespace
  (`FOO=bar ` gives `bar `, as Docker would set it); droot 0.7 trimmed spaces
  and tabs at both ends of each line.
- Device nodes created by `droot run` are owned by root instead of
  `--user`/`--group` (mode 0666 as before), and none are created when `/dev`
  in the container is a mount point.
- Like Docker, `droot run` masks the sensitive files of `/proc` and `/sys`
  (`/proc/kcore`, `/proc/keys`, `/sys/firmware`, ...) and makes `/proc/sys`,
  `/proc/sysrq-trigger`, `/proc/irq`, `/proc/bus`, `/proc/fs` and the whole
  of `/sys` read-only. Applications that change kernel settings (e.g. with
  `sysctl -w`) fail; use `--no-mask` to keep the old behavior.
- `droot run` no longer creates a `.droot.keep` file in empty `--bind` and
  `--robind` source directories, as droot 0.7 did for an undocumented
  reason: droot does not write into the host directories it binds.
- `droot run` sets `no_new_privs` by default: setuid programs (`sudo`, `su`,
  ...) and file capabilities can no longer grant privileges to the command.
  Use `--no-new-privileges=false` for applications that need them.
- `droot run` requires root (euid 0) and refuses `--root /`;
  `droot umount --root /` is refused.
- `DOCKER_CERT_PATH` alone no longer enables TLS: like the docker CLI, TLS is
  used with `DOCKER_TLS_VERIFY` (verifying the daemon's certificate) or
  `DOCKER_TLS` (without verification). droot 0.7 used TLS *without
  verifying the certificate* when only `DOCKER_CERT_PATH` was set, even with
  a `ca.pem`, which let a man in the middle serve a fake daemon and inject
  files into archives later run as root.
- The command line is parsed by droot itself instead of urfave/cli: help
  output changed, and usage errors (unknown commands, flags or help topics)
  print a short hint, with a suggestion, instead of the full help, and exit
  with status 1. Repeatable options (`--env`, `--bind`, ...) never split
  their values on commas. Only `droot help` remains as a help command
  (`droot help run`, `droot run --help`); subcommands have none.
- The options of `droot run` must come before COMMAND: `droot run CMD --root
  R`, which droot 0.7 accepted by moving the flags ahead, is now an error
  (everything after COMMAND belongs to COMMAND).
- Extra positional arguments of `droot export` and `droot umount` are errors
  instead of being ignored.
- `-D`/`--debug` is no longer detected anywhere on the command line: droot
  0.7 also enabled its debug output when COMMAND had a `-D` argument. It may
  now be given before or after the subcommand.
- When both are given, `--debug` takes precedence over `DROOT_DEBUG` (in 0.7,
  `DROOT_DEBUG=0` disabled `-D`).
- `droot export -o -` writes to STDOUT; droot 0.7 created a file named `-`.
- With archives exported by this version, `droot run` uses the image's
  `USER` as the default of `--user` and its `WORKDIR` as the working
  directory, like `docker run`; droot 0.7 always ran as root (unless `--user`
  was given) in `/`. Give `--user root` or `--workdir /` to keep the old
  behavior. Archives exported by droot 0.7 are run as before.
- `droot run` sets `PATH`, `HOME`, `USER` and `LOGNAME` when the image's
  environment and the options do not.
- The Go packages moved under `internal/` (`internal/commands`,
  `internal/docker`, ...): droot is a command, and its packages were never
  meant to be imported by other modules.

### Fixed

- `droot export` works with current Docker releases: the API version is
  negotiated with the daemon instead of being pinned to 1.29, which recent
  daemons reject.
- `droot export` reads the environment from the image configuration. droot
  0.7 used the deprecated `ContainerConfig`, which is empty for BuildKit images
  and missing in recent API versions (droot crashed with a nil pointer
  dereference).
- `droot export` no longer runs the image: images without `/bin/sh`
  (distroless, `FROM scratch`) are supported, and environment values containing
  quotes, `$` or backquotes are preserved verbatim instead of being interpreted
  by a shell.
- `droot export` always removes its temporary container, including after
  errors and Ctrl-C, and writes `-o FILE` atomically. In particular, a write
  to a closed pipe (`droot export img | head`, or a failing `aws s3 cp -`)
  no longer kills droot with SIGPIPE before the cleanup, SIGHUP is handled
  like SIGINT, Ctrl-C works while the reader of stdout is stalled, and a
  second Ctrl-C terminates droot immediately.
- `droot export` accepted a stream cut between two tar entries, or an empty
  `204` answer, as a complete archive. It now requires `200 OK` and the
  end-of-archive marker, which file data ending with zeros can no longer fake. Redirects are no longer followed (they could resend
  requests and the TLS client certificate elsewhere).
- Docker Engine API version handling: a daemon not advertising its version
  got 1.24, which current Docker rejects (now 1.41); `DOCKER_API_VERSION`
  with a leading `v` (`v1.45`) produced bogus URLs; `--platform` was
  silently ignored with API versions older than 1.41 (now an error); and
  every 404 — such as an image lacking the requested platform — was reported
  as "image not found locally".
- Environment variables that `/.drootenv` cannot represent (keys starting
  with `#`, which were silently read back as comments, keys surrounded by
  spaces, multi-line values) are skipped with a warning at export time.
- Images that already contained a `/.drootenv` hard-linked from another
  file, or a `/.drootenv` directory, produced archives that `tar` failed to
  extract. Entries below such a directory are dropped too, and hard links
  to dropped files become regular files with the original content (or
  copies of a dropped symlink, FIFO or device).
- `/proc/self/mountinfo` was split on any white space: mount points
  containing characters the kernel does not escape (`\v`, `\f`, `\r`,
  non-breaking spaces, ...) and mounts with an empty source were misread by
  `droot umount`. Found by fuzzing.
- The environment saved in `/.drootenv` is read from the image's
  configuration instead of the temporary container's, to which Podman adds
  `container=podman` (and reorders the variables).
- `DOCKER_HOST` is parsed like the docker CLI: unix socket paths are taken
  literally (`%`, `#`, `?`, abstract `@name` sockets), `unix://` alone means
  the default socket, and a host without scheme (`192.0.2.1:2375`) is TCP.
- `droot export -o FILE` created the file with mode 0644 regardless of the
  umask (archives may contain secrets such as `/etc/shadow`), replaced the
  mode and owner of an existing file, replaced a symlink instead of writing
  to its target, broke hard links and failed for names close to 255 bytes.
  It now behaves like droot 0.7's `os.Create` while still replacing the file
  atomically (in place only when the file has several hard links).
- `droot run` dropped capabilities and changed credentials on one OS thread
  only and could `exec` from another one, so the command could run with the
  full capability bounding set. The goroutine is now locked to its thread.
- `droot run` only reduced the capability bounding set, which does not limit
  inheritable capabilities: when droot was started with a non-empty
  inheritable set (pam_cap, systemd `CapabilityInheritable=`), the command
  kept those capabilities as root, and a `--user` gained them by executing a
  file with `+i` file capabilities. The inheritable and ambient sets are now
  emptied too.
- Ctrl-C (or SIGTERM/SIGHUP) during the setup of `droot run` was caught and
  ignored, and COMMAND was started anyway. droot now stops the setup, and
  signals arriving after the last check act on droot or COMMAND normally.
- `droot run` created `/dev/zero` with the numbers of `/dev/null` (1:3) and
  `/dev/random` with those of `/dev/urandom` (1:9). Device permissions no longer
  depend on the umask.
- `droot run` looks up `COMMAND` in the container's `PATH` (from `/.drootenv`
  or `--env`) instead of the `PATH` of the sudo environment.
- Symbolic links in an image could make `--cp`, `--bind` and `--robind` write
  to or mount over paths of the host. Paths inside the root are now resolved
  with file descriptors (`openat(2)` with `O_NOFOLLOW`, component by
  component) and every operation is done relative to the resolved directory,
  which also resists a running container swapping directories for symlinks
  while droot works.
- With the host's `/dev` (or a single device) bind mounted into the
  container, `droot run` chmodded the host's `/dev/null`, `/dev/zero`, ...
  and chowned them to `--user`, which could then break the host with
  `chmod 000 /dev/null`.
- Bind mounts of directories living on a shared mount (the default for `/` on
  systemd hosts) joined the host's peer group, so a mount made under them in
  the container — such as `--robind SECRET:/app/secrets` inside
  `--bind H:/app` — propagated back to `H/secrets` on the host, read-write.
  The mounts droot makes are now slave mounts.
- A bind mount target resolving to the root itself (e.g. an image symlink
  `/data -> /` with `--bind H:/data`) mounted the host directory over the
  whole container, and targets resolving through `/proc` or `/sys` created
  directories in the host's kernel file systems (e.g. cgroups). Both are
  now refused.
- The container's `/etc/passwd` and `/etc/group` were opened by path on the
  host: an absolute symlink in the image made droot read the host's files,
  and a FIFO made it hang. They are now resolved inside the root and must be
  regular files.
- `/.drootenv` lines whose value contained `=` (e.g. `JAVA_OPTS=-Da=b`) were
  dropped, and lines longer than 64 KiB silently truncated the environment.
- `--robind` cleared the `nosuid`, `nodev` and `noexec` flags of the source
  mount when remounting it read-only.
- `--robind` failed for empty read-only source directories.
- `droot umount` unmounted by path, following symlinks: a process in the
  container could replace a directory holding a mount point with a symlink
  and make droot detach a mount of the host. Mount points are now opened
  without following symlinks and unmounted through their descriptor after
  checking their mount ID.
- `droot umount` exited successfully while leaving mounts behind when they
  were hidden by another mount; it now retries until nothing is left and
  fails, listing them, if some cannot be reached.
- `droot umount --root /a/app` also unmounted `/a/app2/...`, mishandled mount
  points containing spaces, and missed mounts when the root path contained a
  symbolic link.
- The test suite did not compile (`kylelemons/godebug` missing from `go.mod`)
  and CI used an end-of-life Go version.

### Added

- Docker contexts (`DOCKER_CONTEXT`, `docker context use`, including contexts
  with `skip-tls-verify` and no TLS files), `DOCKER_CONFIG`,
  `DOCKER_API_VERSION`, `https://` hosts and `ssh://` hosts (through `ssh
  HOST docker system dial-stdio`, like the docker CLI). Works with Docker
  Desktop, Colima, OrbStack and Podman's Docker-compatible socket.
- `droot export --pull` pulls the image first, with the credentials of the
  docker CLI (credential helpers, `credsStore`, `auths`), for `--platform` if
  given.
- `droot export --platform` to pick a variant of a multi-platform image.
- `droot export` without a Docker daemon, for the images of registries
  (`docker://NAME[:TAG|@DIGEST]`, with the credentials of the docker CLI),
  `docker save` archives (`docker-archive:FILE`) and OCI image layouts
  (`oci:DIR`, `oci-archive:FILE`): droot applies the layers itself,
  whiteouts included, verifying every digest.
- `droot export` options for the ship step: `--compress gzip|zstd`,
  `--checksum` (`FILE.sha256`), `--reproducible` archives, `--exclude
  PATTERN` and `--drop-docker-files`; the progress is shown on a terminal.
- `droot export` saves the image configuration (`Env`, `WorkingDir`, `User`,
  `Entrypoint`, `Cmd`, `StopSignal`) as `/.droot/config.json`, which, unlike
  `/.drootenv`, represents every environment variable. `droot run` uses it:
  `droot run --root DIR` alone runs the image's `ENTRYPOINT` and `CMD`.
- Rootless `droot run`: run by a user other than root, droot runs itself
  again as root of a user namespace (the user as root, their subordinate IDs
  from `/etc/subuid` and `/etc/subgid` as the other users, with
  `newuidmap`/`newgidmap`), in a private mount namespace, binding the host's
  `/proc` and device files, and supervises `COMMAND` (signals, exit status).
  `droot unshare` runs a command in such a namespace, to extract archives
  with their owners.
- `droot run --workdir`/`-w`.
- `droot run --config FILE` reads the options from a file (a subset of TOML,
  parsed without a new dependency), so that systemd units stay short; see
  `examples/droot/app.toml`.
- `droot run --no-new-privileges` sets the `no_new_privs` flag (now the
  default).
- `droot run --seccomp default` installs a seccomp filter that denies, like
  Docker's default profile, namespace creation, the mount API, module and
  kernel loading, keyrings, `bpf`, perf events, `userfaultfd`, io_uring and
  other host-wide system calls (amd64, arm64, arm and 386).
- `droot run --cap-add` and `--cap-drop` adjust the capabilities kept in the
  bounding set.
- `droot run --env-file FILE` and `--env KEY` (without a value, taking it from
  droot's environment), like `docker run`.
- `droot run --bind SRC:DEST:OPTS` with the options `ro`, `rw` and `rbind`
  (recursive bind mount; read-only recursively with `ro`).
- `droot run` populates `/dev` like Docker: `/dev/full`, `/dev/tty`, a
  private `devpts` instance on `/dev/pts` with the `/dev/ptmx` symlink, a
  tmpfs on `/dev/shm`, and the `/dev/fd`, `/dev/stdin`, `/dev/stdout` and
  `/dev/stderr` symlinks, in addition to the four devices of droot 0.7.
- `droot run --tmpfs PATH[:OPTS]`, e.g. for `/tmp` and `/run`.
- `droot run --ulimit NAME=SOFT[:HARD]` sets resource limits; the README
  shows how to apply cgroup limits with systemd.
- `droot run --unshare` makes the mounts in a private mount namespace, so
  that they disappear with the command; `--unshare-ipc` and `--hostname NAME`
  give it a private IPC or UTS namespace, and `--read-only` a read-only root.
- `droot status --root DIR` lists the mounts below DIR and the processes
  running in it.
- When `droot run` fails before executing COMMAND (a missing bind source, a
  missing command, Ctrl-C, ...), it removes the mounts it made.
- Concurrent `droot run` and `droot umount` on the same root wait for each
  other's setup (`flock(2)` on a file of `/run/droot`, which only root can
  access, so that a container cannot block droot).
- `droot run --bind-net-files` bind mounts the host's `/etc/resolv.conf` and
  `/etc/hosts` read-only, as a live alternative to the copies of `--cp`.
- A warning when `--user NAME` maps to uid 0 in the image.
- Temporary export containers are labeled `io.github.yuuki.droot`.
- Bind mounting single files with `--bind`/`--robind`.
- `--quiet`/`-q` and `--log-format json` (structured messages with
  `log/slog`, e.g. for journald), also set by `DROOT_QUIET` and
  `DROOT_LOG_FORMAT`.
- `droot --version` also prints the commit, its date and the Go version.
- A command line reference (`documentation/cli.md`) and a man page
  (`documentation/droot.1`, in the release archives), generated from the
  command definitions by `make docs`.
- `droot completion bash|zsh|fish` prints a completion script for the
  commands and options, including the users, groups and directories taken by
  `droot run` options, and the command line after `droot run`'s COMMAND.
- `--debug` can be given after the subcommand.
- Helpful error messages: missing images, a command whose ELF/script
  interpreter is missing in the container, writing an archive to a terminal.
- linux/arm64, linux/arm (ARMv7, `droot_linux_armv7`) and darwin/arm64
  binaries and a `checksums.txt` in releases. The assets keep the names and
  layout of 0.7.7 (`droot_linux_amd64.tar.gz`, `droot_darwin_amd64.zip`, bare
  `droot_linux_amd64`, ...); the archives also contain the man page.
- deb and rpm packages for Linux amd64, arm64 and ARMv7
  (`droot_0.8.0-1_amd64.deb`, `droot-0.8.0-1.x86_64.rpm`, ...). They install
  `/usr/bin/droot`, the man page and the bash, zsh and fish completions, and
  have no dependencies.
- Signed SLSA build provenance attestations for every release asset
  (`gh attestation verify`), an SPDX SBOM for every archive, and
  reproducible binaries and archives.
- `SECURITY.md`: how to report vulnerabilities privately.
- Documentation: architecture, security considerations, development guide,
  known issues and ideas, a comparison with other tools (enroot, systemd,
  Podman), and a deployment tutorial with example systemd units
  (`examples/systemd`). A Japanese README (`README.ja.md`).
- CI on GitHub Actions (lint, unit tests as user and root on the minimum and
  latest Go, coverage report, fuzzing, end-to-end tests against Docker on
  amd64 and arm64 and against Podman, govulncheck, a security linter for the
  workflows, release snapshots), Dependabot, issue and pull request
  templates, and a release workflow that runs CI on the tagged commit, builds
  a draft release with GoReleaser and takes its notes from this changelog.

### Removed

- The dependency on `github.com/docker/docker` and its transitive modules
  (`docker/distribution`, `docker/go-connections`, `docker/go-units`,
  `docker/libtrust`, `opencontainers/go-digest`, `sirupsen/logrus`), as well as
  `pkg/errors` and `golang.org/x/net`, and `github.com/urfave/cli` (replaced
  by a small parser in `internal/cli`). droot now depends only on
  `golang.org/x/sys`.
- The goxc/ghr/gobump release scripts and the Dockerfile used to build in a
  container (cross compilation needs no cgo any more).

[Unreleased]: https://github.com/yuuki/droot/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/yuuki/droot/releases/tag/v0.8.0
