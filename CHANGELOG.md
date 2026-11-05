# Changelog

All notable changes to this project are documented in this file. The format
is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Releases up to 0.7.7 are described on the
[GitHub releases page](https://github.com/yuuki/droot/releases).

## [Unreleased]

Maintenance release after several years: modern Go and Docker, far fewer
dependencies, many bug and security fixes. Planned as **v0.8.0**.

### Breaking changes

- Go 1.26 or later is required to build. Release binaries are statically
  linked (`CGO_ENABLED=0`); as a consequence, host user names served only by
  NSS (LDAP, SSSD, ...) are no longer resolved — use `--cp` or numeric IDs.
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
- `droot run` requires root (euid 0) and refuses `--root /`;
  `droot umount --root /` is refused.
- `DOCKER_CERT_PATH` alone no longer enables TLS: like the docker CLI, TLS is
  used with `DOCKER_TLS_VERIFY` (verifying the daemon's certificate) or
  `DOCKER_TLS` (without verification). droot 0.7 used TLS *without
  verifying the certificate* when only `DOCKER_CERT_PATH` was set, even with
  a `ca.pem`, which let a man in the middle serve a fake daemon and inject
  files into archives later run as root.
- Release assets are built with GoReleaser: `droot_<version>_<os>_<arch>.tar.gz`
  archives and bare `droot_<os>_<arch>` binaries for linux and darwin on amd64
  and arm64. 386 builds and zip archives are no longer provided.
- The CLI moved to urfave/cli v3: help output changed, and usage errors print
  a short hint instead of the full help.

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
  end-of-archive marker. Redirects are no longer followed (they could resend
  requests and the TLS client certificate elsewhere).
- Docker Engine API version handling: a daemon not advertising its version
  got 1.24, which current Docker rejects (now 1.41); `DOCKER_API_VERSION`
  with a leading `v` (`v1.45`) produced bogus URLs; `--platform` was
  silently ignored with API versions older than 1.41 (now an error); and
  every 404 — such as an image lacking the requested platform — was reported
  as "image not found locally".
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
- `--env` values containing commas are not split (urfave/cli v2+ default).
- The test suite did not compile (`kylelemons/godebug` missing from `go.mod`)
  and CI used an end-of-life Go version.

### Added

- Docker contexts (`DOCKER_CONTEXT`, `docker context use`, including contexts
  with `skip-tls-verify` and no TLS files), `DOCKER_CONFIG`,
  `DOCKER_API_VERSION` and `https://` hosts. A clear error is printed for
  unsupported `ssh://` hosts. Works with Docker Desktop, Colima, OrbStack and
  Podman's Docker-compatible socket.
- `droot export --platform` to pick a variant of a multi-platform image.
- `droot run --no-new-privileges` sets the `no_new_privs` flag.
- A warning when `--user NAME` maps to uid 0 in the image.
- Temporary export containers are labeled `io.github.yuuki.droot`.
- Bind mounting single files with `--bind`/`--robind`.
- `droot completion bash|zsh|fish`.
- `--debug` can be given after the subcommand.
- Helpful error messages: missing images, a command whose ELF/script
  interpreter is missing in the container, writing an archive to a terminal.
- linux/arm64 and darwin/arm64 binaries.
- Documentation: architecture, security considerations, development guide,
  known issues and ideas.
- CI on GitHub Actions (lint, unit tests as user and root on the minimum and
  latest Go, end-to-end tests against Docker, cross builds), Dependabot,
  GoReleaser release workflow.

### Removed

- The dependency on `github.com/docker/docker` and its transitive modules
  (`docker/distribution`, `docker/go-connections`, `docker/go-units`,
  `docker/libtrust`, `opencontainers/go-digest`, `sirupsen/logrus`), as well as
  `pkg/errors` and `golang.org/x/net`. droot now depends only on
  `github.com/urfave/cli/v3` and `golang.org/x/sys`.
- The goxc/ghr/gobump release scripts and the Dockerfile used to build in a
  container (cross compilation needs no cgo any more).
