droot
=====

English | [日本語](README.ja.md)

[![CI](https://github.com/yuuki/droot/actions/workflows/ci.yml/badge.svg)](https://github.com/yuuki/droot/actions/workflows/ci.yml)
[![Latest Version](https://img.shields.io/github/release/yuuki/droot.svg?style=flat-square)](https://github.com/yuuki/droot/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/yuuki/droot)](https://goreportcard.com/report/github.com/yuuki/droot)
[![License](https://img.shields.io/badge/license-MIT-blue.svg?style=flat-square)](LICENSE)

Droot is a simple chroot-based application container engine: build with
`docker build`, ship a plain tar archive, and run it with `chroot(2)` instead of
`docker run`. Droot aims to simply escape dependency hell.

## Overview

[Docker](https://www.docker.com) has a powerful concept about an application
deployment process: Build, Ship, Run. But there are many cases where the Docker
runtime is too complicated and beyond our current capabilities, especially in
production. Droot provides a simpler container runtime without complicated
stacks such as Linux namespaces and union file systems. It relies on simple
Linux features — `chroot(2)`, Linux `capabilities(7)` and bind mounts — to run
a container image built by `docker`, and on plain tar archives to ship it.

- **Build**: build an image with `docker build` (or any tool building OCI images) and turn it into a tar archive with `droot export`, from a Docker daemon, a registry, a `docker save` archive or an OCI layout.
- **Ship**: upload the archive to any storage (Amazon S3, HTTP, rsync, ...) and download it on your servers.
- **Run**: `droot run` = `chroot(2)` + dropped capabilities + bind mounts.

![droot architecture](documentation/images/architecture.png)

Droot deliberately does **not** isolate processes, networks, users or resources
the way Docker does: a droot container shares the host's PID, network, IPC and
UTS namespaces and cgroups. Read [Security considerations](documentation/security.md)
before running untrusted images.

## Requirements

| Command | Requirements |
|---|---|
| `droot export` | Linux or macOS. For the images of a daemon, a daemon speaking the Docker Engine API: Docker Engine, Docker Desktop, Colima, OrbStack, Podman (`podman system service`), ... Images of registries, `docker save` archives and OCI layouts need no daemon. |
| `droot run` | Linux. As root, or as any user with [user namespaces](#running-without-root) (rootless). |
| `droot umount` | Linux, root privileges |

Building from source requires Go 1.26 or later.

## Installation

### Prebuilt binaries

Static binaries for Linux (amd64, arm64 and 32-bit ARMv7, e.g. a Raspberry
Pi with a 32-bit OS) and macOS (amd64 and arm64) are attached to every
[GitHub release](https://github.com/yuuki/droot/releases), both as archives
(`droot_linux_amd64.tar.gz`, `droot_linux_armv7.tar.gz`,
`droot_darwin_arm64.zip`, ...) and as bare binaries:

```console
$ curl -fsSL -o droot https://github.com/yuuki/droot/releases/latest/download/droot_linux_amd64
$ chmod +x droot && sudo mv droot /usr/local/bin/
```

#### Verifying a release

droot runs as root, so check what you install. Every asset has a signed
build provenance attestation, and every archive an SPDX SBOM (`*.sbom.json`).
To check that a download was built by the release workflow of this repository
from a release tag, use the [GitHub CLI](https://cli.github.com):

```console
$ gh attestation verify droot --repo yuuki/droot --signer-workflow yuuki/droot/.github/workflows/release.yml
```

The binaries are reproducible; see
[Releasing](documentation/development.md#releasing) for how they are built,
verified and rebuilt.

### go install

```console
$ go install github.com/yuuki/droot/cmd/droot@latest
```

### Homebrew

```console
$ brew tap yuuki/droot
$ brew install droot
```

> [!WARNING]
> Until the formula of the tap is updated, it installs droot 0.7.7, whose
> `droot export` does not work with current Docker releases (it pins the
> Docker API version 1.29). Prefer the prebuilt binaries or `go install`.

### From source

```console
$ git clone https://github.com/yuuki/droot && cd droot
$ make build      # produces ./droot, statically linked
```

## Quick start

Build an image and export its file system:

```console
$ docker build -t dockerfiles/app .
$ droot export --compress gzip --checksum -o app.tar.gz dockerfiles/app
$ aws s3 cp app.tar.gz s3://drootexamples/app.tar.gz
$ aws s3 cp app.tar.gz.sha256 s3://drootexamples/app.tar.gz.sha256
```

Deploy and run it on a server (extract as root so that file ownership and
setuid bits are preserved):

```console
$ aws s3 cp s3://drootexamples/app.tar.gz . && aws s3 cp s3://drootexamples/app.tar.gz.sha256 .
$ sha256sum -c app.tar.gz.sha256
$ sudo mkdir -p /var/containers/app
$ sudo tar -xpzf app.tar.gz -C /var/containers/app
$ sudo droot run --cp --user appuser --group appgroup --bind /var/log/app --root /var/containers/app -- command
```

`droot run` leaves its mounts in place after the command exits (unless
`--unshare` is given). Remove them before deleting or replacing the directory,
for instance to deploy a new version — deleting it with mounts below would
delete files of the host through the bind mounts:

```console
$ sudo droot umount --root /var/containers/app
```

To deploy new versions, extract each one into a new directory and switch
between them, e.g. with capze (below).

See [Deploying an application](documentation/deployment.md) for a complete
walkthrough, including systemd units.

### Integration with capze

[capze](https://github.com/yuuki/capze) deploys archives in a Capistrano-like
directory layout and keeps the last releases:

```console
$ sudo mkdir /tmp/app && sudo tar -xpzf app.tar.gz -C /tmp/app   # after verifying the checksum as above
$ sudo capze --keep 5 /tmp/app /var/containers/app
```

## Commands

Run `droot COMMAND --help` for the full list of options.

### `droot export`

```
droot export [-o FILE] [--platform PLATFORM] IMAGE
```

Exports the file system of a container image as a tar archive, to STDOUT or
to `FILE`. The image's configuration — `ENV`, `WORKDIR`, `USER`,
`ENTRYPOINT`, `CMD` and `STOPSIGNAL` — is saved as `/.droot/config.json` in
the archive, and its environment variables also as `/.drootenv` (read by older
droot versions), so that `droot run` can use them. Images without a shell,
such as distroless or `FROM scratch` images, are supported.

`IMAGE` is an image of a Docker daemon (`NAME[:TAG]`, an ID, ...): droot
creates a temporary container from it **without starting it**, streams its
file system and removes the container. Or it is one of these images, read
[without daemon](#exporting-without-a-docker-daemon):

| `IMAGE` | |
|---|---|
| `docker://[REGISTRY/]NAME[:TAG\|@DIGEST]` | An image of a registry: Docker Hub (`docker://alpine:3.22`), `ghcr.io`, ... |
| `docker-archive:FILE[:NAME:TAG]` | An archive written by `docker save` or `podman save`. |
| `oci:DIR[:NAME]` | An [OCI image layout](https://github.com/opencontainers/image-spec/blob/main/image-layout.md), as written by `skopeo`, `buildah`, `docker buildx build --output type=oci,tar=false`, ... |
| `oci-archive:FILE[:NAME]` | A tar archive of an OCI image layout (`docker save` of Docker 25 and later, `podman save --format oci-archive`, ...). |

An image of a daemon whose name starts like one of these (`oci:latest`)
needs its full name: `docker.io/library/oci:latest`.

| Option | Description |
|---|---|
| `-o`, `--output FILE` | Write the archive to `FILE` instead of STDOUT (`-` means STDOUT). The file is replaced atomically, so an interrupted export never leaves a truncated archive behind; like a plain `> FILE` redirection, a new file follows the umask, an existing file keeps its mode and owner, and symlinks are followed. |
| `--platform PLATFORM` | Export the given variant (e.g. `linux/amd64`, `linux/arm/v7`) of a multi-platform image. By default, the variant of the daemon's platform, or, without daemon, of the platform droot runs on. |
| `--pull` | For an image of a daemon: pull `IMAGE` from its registry into the daemon first (the `--platform` variant, if given), as `docker pull` would, with the credentials of the docker CLI: the credential helpers (`credHelpers`, `credsStore`) and the `auths` of `$DOCKER_CONFIG/config.json`. Without a tag, `latest` is pulled. |
| `--compress ALGO` | Compress the archive with `gzip`, or `zstd` (with the `zstd` command). |
| `--checksum` | Also write the SHA-256 checksum of the archive (as written, compressed or not) to `FILE.sha256`, in the format of `sha256sum`; needs `-o FILE`. Verify it with `sha256sum -c FILE.sha256` before extracting the archive as root. |
| `--reproducible` | Make the archive depend only on the image, so that exporting the same image again gives the same bytes (for caches, deduplication or signatures): modification times later than `$SOURCE_DATE_EPOCH`, or else the image's creation time, are set to it (the files Docker creates in every container and the files added by droot are dated from it), and access and change times are dropped. Entries keep the order of the daemon (or of the layers). |
| `--exclude PATTERN` | Leave out the entries matching `PATTERN`, and everything below a matching directory. A pattern containing `/` is matched against absolute paths (`/usr/share/doc`, `/var/cache/apk/*`), a pattern without `/` against base names (`*.pyc`); the syntax is that of Go's [`path.Match`](https://pkg.go.dev/path#Match). A hard link to a left out file is left out too, with a warning. Repeatable. |
| `--drop-docker-files` | Leave out the files Docker adds to every container: `/.dockerenv`, and the empty placeholders `/etc/hostname`, `/etc/hosts`, `/etc/resolv.conf` and `/dev/console` (use `--cp` or `--bind-net-files` with `droot run`). Images read without daemon have none of them. |

For an image of a daemon, without `--pull`, the image must exist locally
(`docker pull` or `docker build` it first).
droot refuses to write the archive to a terminal. When STDERR is a terminal,
the size exported so far is shown there.

To sign archives, sign the checksum file with your tool of choice, e.g.
`cosign sign-blob --bundle app.tar.gz.sha256.sigstore.json app.tar.gz.sha256`
or `minisign -Sm app.tar.gz.sha256`, and verify the signature, then the
checksum, before extracting.

### `droot run`

```
droot run --root ROOT_DIR [OPTIONS] [--] [COMMAND [ARG...]]
```

Runs `COMMAND` inside `ROOT_DIR` (an extracted archive). Without `COMMAND`,
the image's `ENTRYPOINT` followed by its `CMD` is run. Unlike `docker run`,
a `COMMAND` replaces both: give the entrypoint explicitly to pass it
arguments. The image's `USER` and `WORKDIR` are the defaults of `--user` and
`--workdir`. These defaults come from `/.droot/config.json`, written by
`droot export` since droot 0.8; with archives exported by older versions,
`COMMAND` is required and droot runs as root in `/` unless told otherwise.

In order, droot:

1. optionally copies the host's `/etc/group`, `/etc/passwd`, `/etc/resolv.conf` and `/etc/hosts` into `ROOT_DIR` (`--cp`);
2. mounts `proc` on `/proc` and bind mounts the host's `/sys` on `/sys` (as an rslave mount), masks their sensitive files and makes `/proc/sys`, `/proc/sysrq-trigger`, `/proc/irq`, `/proc/bus`, `/proc/fs` and `/sys` read-only like Docker (unless `--no-mask`), and performs the `--bind`/`--robind`/`--tmpfs` mounts (parents first) and the `--bind-net-files` mounts;
3. creates the `/var/lock -> ../run/lock` symlink if `/var` exists and `/var/lock` does not, and populates `/dev` like Docker — unless `/dev` is a mount point (e.g. `--bind /dev`), in which case it is left alone: the devices `null`, `zero`, `full`, `random`, `urandom` and `tty` (owned by root, mode 0666), the symlinks `fd -> /proc/self/fd`, `stdin`, `stdout`, `stderr` and `ptmx -> pts/ptmx`, a private `devpts` instance on `/dev/pts` (pseudo-terminals) and a 64 MiB tmpfs on `/dev/shm`;
4. `chroot`s into `ROOT_DIR`;
5. drops every capability except `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`, `CAP_SETGID`, `CAP_SETUID` and `CAP_NET_BIND_SERVICE` (adjusted by `--cap-add`/`--cap-drop`) from the bounding set and empties the inheritable and ambient sets (unless `--no-dropcaps`);
6. switches to the requested user and groups;
7. changes to the working directory and executes `COMMAND` with the environment of the image (the `Env` of `/.droot/config.json` overridden by `/.drootenv`), overridden by `--env-file`, then by `--env`. `PATH` (`/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`), `HOME`, `USER` and `LOGNAME` (from the user's entry in `ROOT_DIR/etc/passwd`; `HOME=/` without entry) are set when missing. droot *replaces itself* with `COMMAND`, so the exit status and signals are those of `COMMAND`.

| Option | Description |
|---|---|
| `--config FILE` | Read the options from `FILE`, e.g. `/etc/droot/app.toml`, so that a systemd unit only needs `droot run --config FILE` (see [`examples/droot/app.toml`](examples/droot/app.toml)). Keys are the long names of the options (`root`, `user`, `bind`, `unshare`, ...) and `command` gives `COMMAND`; values are strings, `true`/`false` or arrays of strings, in a subset of TOML (anything else is an error). Options given on the command line take precedence; repeatable ones are added to the file's. |
| `-r`, `--root ROOT_DIR` | Root directory to chroot into (required, on the command line or in `--config`). |
| `-u`, `--user USER` | User name or ID to run as. Defaults to the image's `USER` (which may also name a group, as `USER app:staff`), or to droot's own user (root). Names are looked up in `ROOT_DIR/etc/passwd` first, then on the host (`/etc/passwd`, then `getent passwd` for users served by NSS such as LDAP or SSSD), so the image decides what a name maps to (droot warns when a name other than `root` maps to uid 0); numeric IDs do not depend on the image. Unknown numeric IDs are accepted. |
| `-g`, `--group GROUP` | Group name or ID to run as, looked up like `--user` (`ROOT_DIR/etc/group`, the host's `/etc/group`, `getent group`). Defaults to the primary group of `--user`; for a numeric `--user` without an entry in `ROOT_DIR/etc/passwd`, to droot's own group (usually 0, with a warning), like Docker. |
| `-w`, `--workdir DIR` | Working directory of `COMMAND`, an absolute path inside the container. Defaults to the image's `WORKDIR`, or `/`. It must exist. |
| `-b`, `--bind SRC[:DEST][:OPTS]` | Bind mount the host's `SRC` (directory or file) on `DEST` (default: `SRC`) in the container. `OPTS` is a comma-separated list of `ro` (read-only), `rw` (the default) and `rbind` (also bind the mounts below `SRC`; with `ro`, they are all read-only), as in `--bind /srv/data:/data:ro` or `--bind /srv:rbind`. A last part that is not such a list belongs to `DEST` (`--bind /a:/b:c` mounts on `/b:c`). Repeatable. |
| `--robind SRC[:DEST][:OPTS]` | Same as `--bind SRC:DEST:ro`. Repeatable. |
| `--tmpfs PATH[:OPTS]` | Mount a new tmpfs on `PATH` in the container, e.g. `--tmpfs /tmp --tmpfs /run`. `OPTS` is a comma-separated list of mount flags — `ro`, `rw`, `nosuid`/`suid`, `nodev`/`dev`, `noexec`/`exec`, `noatime`; the default is `nosuid,nodev,noexec` — and [tmpfs options](https://man7.org/linux/man-pages/man5/tmpfs.5.html) such as `size=64m`, `mode=1777`, `uid=`, `gid=`. `--tmpfs /dev/shm:OPTS` replaces the default `/dev/shm`. Repeatable. |
| `--cp`, `--copy-files` | Copy the host's `/etc/group`, `/etc/passwd`, `/etc/resolv.conf` and `/etc/hosts` into the container. |
| `--bind-net-files` | Bind mount the host's `/etc/resolv.conf` and `/etc/hosts` read-only on the same paths in the container, so that `COMMAND` sees later changes to them (e.g. after a DHCP renewal) instead of the snapshot copied by `--cp`. Each run binds the current files afresh; note that a file bind mount follows changes made *in place*: a host file replaced with `rename(2)` while `COMMAND` runs (some DHCP clients, `resolvconf`) is only seen by the next run. With `--cp`, only `/etc/group` and `/etc/passwd` are copied. |
| `-e`, `--env KEY[=VALUE]` | Set an environment variable, overriding `/.drootenv` and `--env-file`. A bare `KEY` takes its value from droot's own environment and is left out if unset, like `docker run -e KEY` (under `sudo`, keep it with `sudo --preserve-env=KEY`). Repeatable. Commas are not separators. |
| `--env-file FILE` | Read environment variables from the host's `FILE`: `KEY=VALUE` lines (the format of `/.drootenv`; `#` starts a comment line) and bare `KEY` lines, taken from droot's environment. Overrides `/.drootenv`; later files override earlier ones. Repeatable. |
| `--cap-add CAP` | Also keep the capability `CAP` in the bounding set: a name with or without `CAP_`, in any case (`NET_RAW`, `cap_sys_ptrace`), or `ALL`. Repeatable. |
| `--cap-drop CAP` | Drop `CAP` from the capabilities kept by default, or `ALL` of them (then `--cap-add` adds back). Repeatable. |
| `--no-dropcaps` | Do not drop capabilities (dangerous). |
| `--ulimit NAME=SOFT[:HARD]` | Set a resource limit of `COMMAND` (`setrlimit(2)`), with the names of `docker run --ulimit`: `nofile`, `nproc`, `core`, `memlock`, `stack`, `as`, `cpu`, ... `HARD` defaults to `SOFT`; `unlimited` or `-1` means no limit. Repeatable. |
| `--unshare` | Make the mounts in a private mount namespace: they are not visible on the host, disappear when `COMMAND` and its children have exited, and need no `droot umount`. Host mounts made later still propagate into the container. |
| `--unshare-ipc` | Run `COMMAND` in a private IPC namespace (System V IPC objects, POSIX message queues). |
| `--hostname NAME` | Run `COMMAND` in a private UTS namespace whose host name is `NAME` (also the default of `HOSTNAME`). The host's name is not changed. |
| `--read-only` | Make `ROOT_DIR` read-only for `COMMAND` (bind mounts and tmpfs keep their own mode: add `--tmpfs /tmp`, `--bind`, ... for what it writes). Implies `--unshare`, so that the read-only mount disappears with `COMMAND`. |
| `--no-mask` | Leave `/proc` and `/sys` as they are: do not mask their sensitive files nor make `/proc/sys`, `/sys` and the like read-only (dangerous). Masks made by a previous run stay until `droot umount`. |
| `--no-new-privileges` | Set the `no_new_privs` flag: setuid programs (`su`, `sudo`, ...) and file capabilities cannot grant privileges to `COMMAND`. On by default; `--no-new-privileges=false` turns it off for applications that need `sudo` or `su`. |
| `--seccomp PROFILE` | `default` installs a seccomp filter that denies, like Docker's default profile, the system calls an application has no business making: creating namespaces (`unshare`, `setns`, `clone` with namespace flags), mounting, loading modules or kernels, keyrings, `bpf`, perf events, `userfaultfd`, io_uring, setting clocks, swapping, rebooting, ... They fail with `EPERM` (`clone3` with `ENOSYS`, so that the C library uses `clone`). Supported on amd64, arm64, arm and 386; programs of another architecture (e.g. 32-bit programs on a 64-bit kernel) cannot make any system call. `unconfined`, the default, installs none. |

Notes:

- Paths given to `--bind`/`--robind` and the files copied by `--cp` are resolved
  *inside* `ROOT_DIR`: symbolic links in the image cannot redirect them to the
  host's file system. A bind mount target may not be `ROOT_DIR` itself nor lie
  inside `/proc` or `/sys`.
- The supplementary groups are those listing the user in `ROOT_DIR/etc/group`.
- Mounts left by a previous run are reused as they are: run `droot umount`
  first to change the options of a `--tmpfs` or a `--bind`.
- If the setup fails, or `COMMAND` cannot be found or executed by the user,
  the mounts made by the failed run are removed again; those of previous runs
  stay. Only a failure of `execve(2)` itself after switching to the user
  (e.g. a missing ELF interpreter) leaves them, with a warning.
- Concurrent `droot run` and `droot umount` on the same `ROOT_DIR` take turns
  until `COMMAND` is executed (`flock(2)` on a file of `/run/droot`, which
  only root can access).
- `COMMAND` is looked up in the `PATH` of the container environment (falling
  back to `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`), not in
  the `PATH` of the shell that started droot.
- The image's `STOPSIGNAL` is saved but not used: droot is not a supervisor.
  Set it as `KillSignal=` in a systemd unit, for instance.
- With `--cp`, the container's `/etc/passwd` and `/etc/group` are the host's,
  so the image's `USER` must exist on the host too, or be overridden with
  `--user`.

#### Resource limits

droot sets the per-process limits of `--ulimit` but creates no cgroups.
Limit memory, CPU and the number of tasks with your service manager, which
already manages cgroups — for a one-off command:

```console
$ sudo systemd-run --scope -p MemoryMax=1G -p CPUQuota=50% -p TasksMax=512 \
    droot run --root /var/containers/app -- app
```

or in the unit running the application:

```ini
[Service]
ExecStart=/usr/local/bin/droot run --root /var/containers/app --user app -- app
MemoryMax=1G
CPUQuota=50%
TasksMax=512
LimitNOFILE=65536
```

`LimitNOFILE=` and the other `Limit*=` settings of the unit are inherited by
`COMMAND` too, unless `--ulimit` overrides them.

### `droot umount`

```
droot umount --root ROOT_DIR
```

Lazily unmounts every mount point below `ROOT_DIR`, deepest first, and fails
if some cannot be unmounted. `ROOT_DIR` itself is never unmounted, so it can
be a file system of its own. The command is idempotent and refuses `/`.

### `droot status`

```
droot status --root ROOT_DIR
```

Lists the mount points below `ROOT_DIR` (what `droot umount` would remove) and
the processes whose root directory is `ROOT_DIR` — `COMMAND`s started by
`droot run` and their children — with their PID, uid and command line:

```console
$ sudo droot status --root /var/containers/app
Mounts below /var/containers/app: 17
  MOUNT POINT      TYPE      SOURCE     OPTIONS
  /dev/pts         devpts    devpts     rw,nosuid,noexec,relatime
  /dev/shm         tmpfs     tmpfs      rw,nosuid,nodev,noexec,relatime
  /proc            proc      proc       rw,nosuid,nodev,noexec,relatime
  /proc/acpi       tmpfs     tmpfs      ro,nosuid,nodev,noexec,relatime
  /proc/keys       devtmpfs  devtmpfs   ro,relatime
  ...
  /sys             sysfs     sysfs      ro,relatime
  /var/log/app     ext4      /dev/sda1  rw,relatime

Processes in /var/containers/app: 1
  PID   UID   COMMAND
  4242  1500  /usr/local/bin/app --port 8080
```

Processes of a `droot run --unshare` (or rootless) are listed, but not their
mounts, which are private.

### `droot unshare`

```
droot unshare [COMMAND [ARG...]]
```

For users other than root: runs `COMMAND` (default: `$SHELL`) as root in a
user namespace with the ID mapping of [rootless](#running-without-root) `droot
run`, so that archives keep the owners of their files:

```console
$ droot unshare tar -xpf app.tar -C app
$ droot unshare rm -rf app      # files owned by subordinate IDs
```

### Global options

| Option | Description |
|---|---|
| `-D`, `--debug` | Print debug messages to STDERR. Also enabled by `DROOT_DEBUG=1`. |
| `-q`, `--quiet` | Print errors only: no warnings, informational messages nor progress. Also enabled by `DROOT_QUIET=1`. |
| `--log-format FORMAT` | Format of the messages on STDERR: `text` (the default) or `json`, one [`log/slog`](https://pkg.go.dev/log/slog) JSON object per line (`{"time":...,"level":"WARN","msg":...}`) for log collectors. Also set by `DROOT_LOG_FORMAT`. |
| `-v`, `--version` | Print the version, with the commit and its date. |

The [command line reference](documentation/cli.md) and the man page
([`documentation/droot.1`](documentation/droot.1), also in the release
archives: `man -l droot.1`) list every option.

### Shell completion

`droot completion bash|zsh|fish` prints a completion script:

```console
$ source <(droot completion bash)     # in ~/.bashrc
$ source <(droot completion zsh)      # in ~/.zshrc, after compinit
$ droot completion fish > ~/.config/fish/completions/droot.fish
```

## Running without root

`droot run`, started by a user other than root, runs **rootless**: droot
runs itself again as root of a new
[user namespace](https://man7.org/linux/man-pages/man7/user_namespaces.7.html),
where it has the privileges to mount and chroot over that namespace only, and
waits for `COMMAND`, passing the signals it receives on to it and exiting
with its status. In the namespace:

- your user is root, and your **subordinate IDs** — the ranges of
  `/etc/subuid` and `/etc/subgid`, mapped with the `newuidmap` and
  `newgidmap` commands (package `uidmap` on Debian and Ubuntu,
  `shadow-utils` on Fedora) — are the users 1, 2, ... Without them, root is
  the only user: run as `--user 0`, and extract archives with plain `tar
  -xf`;
- the mounts are made in a private mount namespace, as with `--unshare`:
  they disappear with `COMMAND`, `droot umount` is not needed;
- `/proc` and the device files are bind mounts of the host's: a new procfs
  needs a PID namespace, device nodes cannot be created;
- the files keep their owners: extract archives with `droot unshare tar -xpf`
  so that the users of the image own their files, and delete them with
  `droot unshare rm -rf`. As root, extract them with `tar -xpf` as usual.

```console
$ grep "^$USER:" /etc/subuid /etc/subgid
/etc/subuid:alice:100000:65536
/etc/subgid:alice:100000:65536
$ mkdir app && droot unshare tar -xpf app.tar -C app
$ droot run --root app --bind ~/app-data:/data
```

Rootless runs cannot raise hard resource limits (`--ulimit`), and bind mount
only what your user can read. Some distributions restrict unprivileged user
namespaces: Ubuntu 24.04 and later deny them capabilities unless
`kernel.apparmor_restrict_unprivileged_userns` is 0 or an AppArmor profile
allows droot; elsewhere, check `user.max_user_namespaces` and
`kernel.unprivileged_userns_clone`. Locks are taken in
`$XDG_RUNTIME_DIR/droot`.

## Exporting without a Docker daemon

With a `docker://`, `docker-archive:`, `oci:` or `oci-archive:` image, `droot
export` needs no daemon: on a CI runner without Docker, from the store of
Podman, Buildah or containerd (through their `save` or `push` commands), or
straight from a registry. droot then reads the manifest of the image (the
variant for `--platform` in a multi-platform image), applies its layers one
after the other like a container engine does — the whiteouts of a layer
delete the files of the layers below it — and writes the resulting file
system.

```console
$ droot export -o app.tar docker://ghcr.io/example/app:1.2.3
$ droot export --platform linux/arm64 -o app-arm64.tar docker://registry.example.com/app@sha256:4f2c…
$ docker save app:1.2.3 -o app-image.tar && droot export -o app.tar docker-archive:app-image.tar
$ droot export -o app.tar oci:build/oci-layout:1.2.3
```

- The digests of the manifests, of the configuration and of the layers
  (stored and uncompressed) are verified; a reference by digest
  (`NAME@sha256:…`) pins the image.
- Registries are accessed with the credentials of the docker CLI (`docker
  login`), as with `--pull`. `localhost` and loopback registries may serve
  plain HTTP; any other registry must serve HTTPS. `HTTPS_PROXY` and
  `NO_PROXY` are honored.
- The layers of a registry are downloaded to `$TMPDIR` (default `/tmp`)
  during the export, then deleted. Layers compressed with zstd need the
  `zstd` command.
- The archive has the files of the image only: not those a daemon adds to
  every container (`/.dockerenv`, the placeholders `/etc/hostname`,
  `/etc/hosts` and `/etc/resolv.conf`, `/dev/console`, ...), and the image's
  own `/etc/hosts` or `/etc/hostname`, if it has some, are kept.
- With several images in an archive or a layout, name one:
  `docker-archive:images.tar:app:1.2.3`, `oci:DIR:1.2.3`.

## Choosing the Docker daemon

`droot export` finds the daemon like the `docker` CLI does:

1. `DOCKER_HOST` (`unix:///path/to/docker.sock`, `tcp://host:2376`,
   `ssh://[user@]host[:port]`), with
   `DOCKER_TLS_VERIFY` (TLS, verifying the daemon's certificate), `DOCKER_TLS`
   (TLS without verification) and `DOCKER_CERT_PATH` (`ca.pem`, `cert.pem`,
   `key.pem`; default `~/.docker`);
2. otherwise the [docker context](https://docs.docker.com/engine/manage-resources/contexts/)
   named by `DOCKER_CONTEXT`;
3. otherwise the current docker context (`docker context use ...`), read from
   `$DOCKER_CONFIG/config.json` (default `~/.docker`);
4. otherwise `unix:///var/run/docker.sock`.

`DOCKER_API_VERSION` pins the API version; by default droot uses the version
advertised by the daemon. For example:

```console
$ DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock droot export -o app.tar localhost/app   # Podman
$ DOCKER_HOST=tcp://192.0.2.10:2376 DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=~/.docker/remote droot export app > app.tar
```

With an `ssh://` host, droot runs `ssh [-l user] [-p port] host docker system
dial-stdio` for each connection, like the docker CLI: the local `ssh` must log
in without a password prompt (keys, an agent), and the remote host needs the
`docker` command.

```console
$ DOCKER_HOST=ssh://deploy@build.example.com droot export -o app.tar app
```

## Documentation

- [Deploying an application](documentation/deployment.md): from `docker build`
  to a systemd service, with [example units](examples/systemd)
- [Command line reference](documentation/cli.md)
- [How droot works](documentation/architecture.md)
- [Security considerations](documentation/security.md)
- [Development guide](documentation/development.md)
- [Known issues and ideas](documentation/roadmap.md)
- [Changelog](CHANGELOG.md)

## Presentations (Japanese)

- [Dockerとchrootを組み合わせたシンプルなコンテナデプロイツール](http://blog.yuuk.io/entry/droot)
- [Droot Internals](https://speakerdeck.com/yuukit/droot-internals)

## Contribution

1. Fork ([https://github.com/yuuki/droot/fork](https://github.com/yuuki/droot/fork))
1. Create a feature branch
1. Commit your changes
1. Rebase your local changes against the master branch
1. Run `make lint test` (and `make integration-test` if you changed `run`, `umount` or `export`) and confirm that they pass
1. Create a new Pull Request

See the [development guide](documentation/development.md) for details.

## License

[MIT](LICENSE). The licenses of the dependencies are listed in
[LICENSE_OF_DEPENDENCIES.md](LICENSE_OF_DEPENDENCIES.md).

## Author

[y_uuki](https://github.com/yuuki)
