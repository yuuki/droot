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
  the host: root keeps `CAP_DAC_OVERRIDE`, `CAP_CHOWN` and `CAP_FOWNER`
  (unless `--cap-drop`), and `/proc` shows the host's processes. Idea: a PID
  namespace.
- **[security]** The seccomp filter is opt-in (`--seccomp default`), is a
  denylist rather than Docker's allowlist, and there is no LSM (AppArmor,
  SELinux) profile.
- **[ux]** By default, mounts are made in the host's mount namespace and
  persist after the command exits: users must remember `droot umount`, and
  concurrent runs share mounts (`--unshare` avoids both).
- **[compat]** `STOPSIGNAL` is saved but not used (droot does not supervise
  COMMAND), and `HOSTNAME` is only set with `--hostname`.
- **[compat]** Nothing is created in `/dev` when it is a mount point, including
  `--tmpfs /dev`; a fresh `/dev` like Docker's would need droot to tell its own
  tmpfs from the host's `devtmpfs`.
- **[compat]** Rootless, `/proc` is the host's (droot has no PID namespace),
  the device files are bind mounts of the host's, hard resource limits
  cannot be raised, and `droot status` does not see the mounts (they are in
  the private namespace of the run). Without subordinate IDs, only root
  exists in the namespace.
- **[compat]** Rootless, the supervising droot relays the notifications of
  `COMMAND` only: those of its children are dropped, even with
  `NotifyAccess=all` (droot cannot send them with their own credentials),
  unless systemd's socket is bound explicitly (`--robind $NOTIFY_SOCKET`).
  An abstract `NOTIFY_SOCKET` is not relayed, nor are the notifications
  when no directory for the relay is writable: they need
  `NotifyAccess=all`. If the supervising droot is killed before the second
  stage has bound the relay, its directory is left behind.
- **[compat]** An explicit `--bind` on the path of `NOTIFY_SOCKET` only
  takes precedence when it names the same path: through a symlink of the
  image (`--bind X:/var/run/systemd/notify` with `/var/run -> /run`),
  the socket replaces it.

### `droot export`

- **[compat]** Without daemon, only `sha256` digests, `localhost` registries
  over plain HTTP (no `insecure-registries`), no registry mirrors and no
  foreign (non-distributable) layers; archives must be uncompressed files
  (not STDIN). Image signatures (cosign, Notation) are not verified.
- **[compat]** `npipe://` `DOCKER_HOST`s (Windows) are not supported.
- **[compat]** Environment variables with multi-line values or keys starting
  with `#` cannot be represented in `/.drootenv`: they are only in
  `/.droot/config.json`, which droot 0.7 does not read (a warning is printed
  at export time).
- **[security]** droot checksums archives (`--checksum`) but does not sign
  them; the README shows how to sign the checksum file with cosign or
  minisign.

### Project

- **[project]** The Homebrew tap (`yuuki/homebrew-droot`) is not updated by
  the release workflow and must be checked against the new asset names.
- **[project]** Windows is not supported at all.

## Feature ideas

### Running

- **[feature]** A PID namespace (`--unshare` covers the mount, IPC and UTS
  namespaces): droot would have to stay as the parent of COMMAND instead of
  replacing itself with it.
- **[feature]** Custom seccomp profiles (Docker's JSON format), and the
  32-bit compatibility system calls of 64-bit kernels.
- **[feature]** Bind propagation modes (`shared`, `private`, ...); droot makes
  every mount a slave mount today.
- **[feature]** cgroup v2 limits (`--memory`, `--cpus`, `--pids-limit`) without
  systemd; who removes the cgroup after the command exits must be decided.
  Today: `--ulimit`, and systemd (`systemd-run --scope`, `MemoryMax=`).
- **[feature]** `droot umount --all`.

### Building and shipping

- **[feature]** `droot deploy`/`droot import`: download (S3/HTTP), verify a
  checksum or signature (cosign, minisign), extract atomically and switch a
  symlink while keeping N releases (capze-like), then optionally restart a
  systemd unit.

### Project and maintenance

- **[project]** Automate the Homebrew tap with GoReleaser, or drop it.
