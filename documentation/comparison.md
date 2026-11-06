# Comparison with other tools

Droot is not the only tool that runs container images with the simplicity of
`chroot`. This page compares it with [enroot](https://github.com/NVIDIA/enroot),
the closest one, and points to other tools for needs droot does not cover.

The facts about enroot come from its
[documentation](https://github.com/NVIDIA/enroot/tree/master/doc) for version
4.2.1 (June 2026); check it for the current state. Corrections are welcome.

## enroot

Enroot describes itself as "an enhanced unprivileged `chroot(1)`". Like
droot, it is standalone (no daemon), runs Docker images, and deliberately
provides "little to no isolation". The two tools differ in who runs them and
for what:

- **enroot** is made for HPC: unprivileged users of a shared cluster run jobs
  and interactive sessions in images, with built-in GPU (libnvidia-container),
  InfiniBand (MOFED) and Slurm support.
- **droot** is made for deploying services: root, typically systemd, runs a
  long-running application from an image as a real user of the host, on
  servers deployed with ordinary tools (S3, rsync, capze, ...).

### At a glance

| | enroot | droot |
|---|---|---|
| Typical use | Jobs and interactive sessions on HPC clusters | Long-running services on servers, under systemd |
| Privileges | Unprivileged, in a user namespace; no setuid binary | Root, or [rootless](../README.md#running-without-root) in a user namespace |
| Users in the container | The invoking user (added to the image's `/etc/passwd` and `/etc/group`), or root with `--root` | Root: the host's user IDs, unchanged. Rootless: the invoking user as root and its subordinate IDs as the other users |
| Image format | squashfs (`.sqsh`), run directly or unpacked by `enroot create`; self-extracting bundles (`enroot bundle`) | A plain tar archive (optionally gzip or zstd), extracted with `tar` |
| Image sources | `docker://` (registry), `dockerd://` (Docker CLI), `podman://` (Podman CLI) | A Docker Engine API daemon (local, `tcp://`, `ssh://`, docker contexts), `docker://`, `docker-archive:`, `oci:`, `oci-archive:` |
| Container storage | A per-user store (`$ENROOT_DATA_PATH`) managed with `create`, `list`, `remove` | Any directory; deployed and switched with your own tools |
| Root file system | Read-only unless `--rw`; changes to a squashfs image are kept in memory | Writable unless `--read-only` |
| Mounts | In a private mount namespace | In the host's mount namespace, kept after exit, unless `--unshare` (always private when rootless) |
| Namespaces | User and mount; PID, network, IPC and UTS with `--pid`, `--net`, `--ipc`, `--uts` | Mount, IPC and UTS with `--unshare`, `--unshare-ipc`, `--hostname`; user when rootless; no PID or network namespace |
| `/dev` | Restricted to a minimal set with `ENROOT_RESTRICT_DEV` or `--ipc` | A minimal set like Docker's (root); bind mounts of the host's device files (rootless) |
| Hardening | The user namespace: a container never has more privileges than its user | Capability bounding set, `no_new_privs` by default, Docker-like masking of `/proc` and `/sys`, an optional seccomp filter (`--seccomp default`); the user namespace when rootless |
| Configuration | `enroot.conf`; system-wide and per-user `environ.d`, `mounts.d`, `hooks.d`; per-container scripts (`--conf`); `/etc/rc`, `/etc/environment`, `/etc/fstab` in the image | Command line options or a file (`--config`); the image's configuration in `/.droot/config.json` |
| GPU and HPC | Hooks for libnvidia-container, MOFED and Slurm PMI | None |
| Implementation | Shell scripts and C helpers; needs curl, gawk, jq, squashfs-tools and GNU parallel; the `enroot+caps` package lets unprivileged users import images | One static Go binary; optional commands: `zstd`, `newuidmap`/`newgidmap`, `ssh`, `getent` |
| Platforms | Linux | Linux; `droot export` also on macOS |
| License | Apache 2.0 | MIT |

### Where droot differs

- **Services under systemd.** Run by root, droot replaces itself with
  COMMAND (`execve(2)`): the main PID of the unit, the signals it receives
  and its exit status are the application's own, and its resource limits are
  those of the unit (`MemoryMax=`, `LimitNOFILE=`, ...). A unit needs one line,
  `ExecStart=droot run --config FILE` (see
  [Run with systemd](deployment.md#5-run-with-systemd)), and the image's
  `USER`, `WORKDIR`, `ENTRYPOINT`, `CMD` and `ENV` are the defaults of the run.
- **Real users.** Run by root, the container uses the host's user IDs: the
  files the application writes to a `--bind` mount belong to the same user on
  the host, users can come from LDAP or SSSD (`getent`), and images with
  several users (a server and its worker user, a database user, ...) keep the
  owners of their files. Rootless, droot maps the subordinate IDs of
  `/etc/subuid` and `/etc/subgid` with `newuidmap` and `newgidmap`, so these
  images still have their users. Enroot's documentation describes no mapping
  but the invoking user (to itself or to root): without a setuid helper such
  as `newuidmap`, an unprivileged user namespace can only map its creator's
  own ID ([`user_namespaces(7)`](https://man7.org/linux/man-pages/man7/user_namespaces.7.html)).
- **Plain archives.** `droot export` writes a tar archive (optionally
  compressed, [reproducible](../README.md#droot-export), with a SHA-256
  checksum file that you can sign), shipped with any tool and extracted with
  `tar`. Neither the build machine nor the servers need squashfs tools. The
  price: the archive must be extracted (as root, to keep the owners of files)
  before it runs and takes the disk space of the extracted files, where enroot
  runs a squashfs image without extracting it.
- **One binary, more image sources.** `droot export` reads remote daemons
  (`tcp://`, `ssh://`, docker contexts), and without daemon reads registries,
  `docker save` archives and OCI layouts, verifying their digests. It runs
  on macOS build machines too. The servers only need the static `droot`
  binary.
- **Hardening for root.** A droot container usually runs as root on the host,
  so droot itself restricts what it can do: it drops capabilities from the
  bounding set, sets `no_new_privs`, masks sensitive files of `/proc` and
  `/sys` and makes the kernel's settings read-only like Docker, can install a
  seccomp filter, and resolves every path inside the root directory without
  following symlinks out of it. See the [security considerations](security.md). Enroot,
  unprivileged, relies on the user namespace instead.

### Where enroot is the better choice

- GPUs, InfiniBand and Slurm (with NVIDIA's
  [Pyxis](https://github.com/NVIDIA/pyxis) plugin): droot has no integration
  with them.
- Many unprivileged users of a shared machine, each with their own container
  store and configuration, without root or subordinate IDs.
- Running an image without extracting it: a squashfs image starts directly,
  read-only, with its changes kept in memory.
- PID and network namespaces (`--pid`, `--net`): droot has neither (see the
  [known issues and ideas](roadmap.md)).

## Other tools

- **systemd** can give a service a root directory of its own (`RootDirectory=`,
  `RootImage=`, `BindPaths=`) and much of droot's hardening
  (`CapabilityBoundingSet=`, `NoNewPrivileges=`, `SystemCallFilter=`,
  `ProtectProc=`). The two work together: `droot export` makes the root
  directory from an image and `droot run` applies the image's configuration,
  while the unit adds the isolation and limits droot does not provide (see the
  [security considerations](security.md#recommendations)).
- **Podman with Quadlet** runs containers as systemd services with the
  isolation droot avoids (PID and network namespaces, a cgroup per container).
  Choose it when the services need that isolation.
