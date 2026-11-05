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
- **[security]** No seccomp filter or LSM profile, and `no_new_privs` is
  opt-in (`--no-new-privileges`): by default setuid-root binaries in the image
  regain the capabilities left in the bounding set.
- **[ux]** By default, mounts are made in the host's mount namespace and
  persist after the command exits: users must remember `droot umount`, and
  concurrent runs share mounts (`--unshare` avoids both).
- **[compat]** `STOPSIGNAL` is saved but not used (droot does not supervise
  COMMAND), and `HOSTNAME` is not set.
- **[compat]** Nothing is created in `/dev` when it is a mount point, including
  `--tmpfs /dev`; a fresh `/dev` like Docker's would need droot to tell its own
  tmpfs from the host's `devtmpfs`.
- **[ux]** Inherited behavior whose purpose is unclear: `--bind` creates a
  `.droot.keep` file in empty host directories. Decide whether to keep it.
- **[compat]** `droot run` needs real root; rootless operation with user
  namespaces is not supported.

### `droot export`

- **[compat]** A Docker Engine API daemon is required (`--pull` fetches images
  missing locally): `docker save` tarballs and OCI layouts are not supported.
- **[compat]** `npipe://` `DOCKER_HOST`s (Windows) are not supported.
- **[compat]** Environment variables with multi-line values or keys starting
  with `#` cannot be represented in `/.drootenv` and are skipped (with a
  warning). The file has no version marker or escaping.
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
- **[feature]** An optional seccomp profile (e.g. Docker's default); consider
  enabling `no_new_privs` by default (with an opt-out: it breaks `sudo`/`su`).
- **[feature]** Bind propagation modes (`shared`, `private`, ...); droot makes
  every mount a slave mount today.
- **[feature]** cgroup v2 limits (`--memory`, `--cpus`, `--pids-limit`) without
  systemd; who removes the cgroup after the command exits must be decided.
  Today: `--ulimit`, and systemd (`systemd-run --scope`, `MemoryMax=`).
- **[feature]** `droot umount --all`.
- **[feature]** Rootless mode with user namespaces.

### Building and shipping

- **[feature]** Daemonless export: flatten images straight from a registry, a
  `docker save` tarball or an OCI layout (works on CI runners without Docker
  and with Podman/Buildah/containerd stores).
- **[feature]** `droot deploy`/`droot import`: download (S3/HTTP), verify a
  checksum or signature (cosign, minisign), extract atomically and switch a
  symlink while keeping N releases (capze-like), then optionally restart a
  systemd unit.

### Usability

- **[ux]** A declarative file (e.g. `/etc/droot/app.toml`) describing the run
  options of an application, usable from systemd units.
- **[ux]** Example systemd unit files and a deployment tutorial.

### Project and maintenance

- **[project]** Automate the Homebrew tap with GoReleaser, or drop it.
- **[project]** A Japanese README.
