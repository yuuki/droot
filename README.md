droot
=====

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

- **Build**: build an image with `docker build` and turn it into a tar archive with `droot export`.
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
| `droot export` | Linux or macOS, and a daemon speaking the Docker Engine API: Docker Engine, Docker Desktop, Colima, OrbStack, Podman (`podman system service`), ... |
| `droot run`, `droot umount` | Linux, root privileges |

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

droot runs as root, so check what you install. Starting with 0.8.0,
`checksums.txt` is signed by the release workflow with
[cosign](https://docs.sigstore.dev/) keyless signing, every asset has a
[build provenance attestation](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations),
and every archive comes with an SPDX SBOM (`droot_linux_amd64.tar.gz.sbom.json`, ...):

```console
$ V=0.8.0 B=https://github.com/yuuki/droot/releases/download
$ curl -fsSLO "$B/v$V/checksums.txt" -O "$B/v$V/checksums.txt.sigstore.json" -O "$B/v$V/droot_linux_amd64"
$ cosign verify-blob --bundle checksums.txt.sigstore.json \
    --certificate-identity "https://github.com/yuuki/droot/.github/workflows/release.yml@refs/tags/v$V" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    checksums.txt
$ sha256sum --check --ignore-missing checksums.txt
$ gh attestation verify --repo yuuki/droot droot_linux_amd64
```

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
$ droot export dockerfiles/app | gzip -c | aws s3 cp - s3://drootexamples/app.tar.gz
```

Deploy and run it on a server (extract as root so that file ownership and
setuid bits are preserved):

```console
$ sudo droot umount --root /var/containers/app   # it is safe to umount before run if you use --bind
$ sudo mkdir -p /tmp/app /var/containers/app
$ aws s3 cp s3://drootexamples/app.tar.gz - | sudo tar -xpzf - -C /tmp/app
$ sudo mv -T /tmp/app /var/containers/app
$ sudo droot run --cp --user appuser --group appgroup --bind /var/log/app --root /var/containers/app -- command
```

`droot run` leaves its mounts in place after the command exits. Remove them
before deleting or replacing the directory:

```console
$ sudo droot umount --root /var/containers/app
```

### Integration with capze

[capze](https://github.com/yuuki/capze) deploys archives in a Capistrano-like
directory layout and keeps the last releases:

```console
$ aws s3 cp s3://drootexamples/app.tar.gz - | sudo tar -xpzf - -C /tmp/app
$ sudo capze --keep 5 /tmp/app /var/containers/app
```

## Commands

Run `droot COMMAND --help` for the full list of options.

### `droot export`

```
droot export [-o FILE] [--platform PLATFORM] IMAGE[:TAG]
```

Exports the file system of a local Docker image as a tar archive, to STDOUT or
to `FILE`. droot creates a temporary container from the image **without
starting it**, streams its file system and removes the container. The image's
configuration — `ENV`, `WORKDIR`, `USER`, `ENTRYPOINT`, `CMD` and
`STOPSIGNAL` — is saved as `/.droot/config.json` in the archive, and its
environment variables also as `/.drootenv` (read by older droot versions), so
that `droot run` can use them. Images without a shell, such as distroless or
`FROM scratch` images, are supported.

| Option | Description |
|---|---|
| `-o`, `--output FILE` | Write the archive to `FILE` instead of STDOUT (`-` means STDOUT). The file is replaced atomically, so an interrupted export never leaves a truncated archive behind; like a plain `> FILE` redirection, a new file follows the umask, an existing file keeps its mode and owner, and symlinks are followed. |
| `--platform PLATFORM` | Export the given variant (e.g. `linux/amd64`) of a multi-platform image. |

The image must exist locally (`docker pull` or `docker build` it first).
droot refuses to write the archive to a terminal.

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
2. mounts `proc` on `/proc`, bind mounts the host's `/sys` on `/sys` (as an rslave mount) and performs the `--bind`/`--robind`/`--bind-net-files` mounts;
3. creates `/dev/null`, `/dev/zero`, `/dev/random`, `/dev/urandom` (unless `/dev` is a mount point, e.g. `--bind /dev`) and the `/var/lock -> ../run/lock` symlink;
4. `chroot`s into `ROOT_DIR`;
5. drops every capability except `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`, `CAP_SETGID`, `CAP_SETUID` and `CAP_NET_BIND_SERVICE` from the bounding set and empties the inheritable and ambient sets (unless `--no-dropcaps`);
6. switches to the requested user and groups;
7. changes to the working directory and executes `COMMAND` with the environment of the image (the `Env` of `/.droot/config.json` overridden by `/.drootenv`), overridden by `--env-file`, then by `--env`. `PATH` (`/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`), `HOME`, `USER` and `LOGNAME` (from the user's entry in `ROOT_DIR/etc/passwd`; `HOME=/` without entry) are set when missing. droot *replaces itself* with `COMMAND`, so the exit status and signals are those of `COMMAND`.

| Option | Description |
|---|---|
| `-r`, `--root ROOT_DIR` | Root directory to chroot into (required). |
| `-u`, `--user USER` | User name or ID to run as. Defaults to the image's `USER` (which may also name a group, as `USER app:staff`), or to droot's own user (root). Names are looked up in `ROOT_DIR/etc/passwd` first, then on the host (`/etc/passwd`, then `getent passwd` for users served by NSS such as LDAP or SSSD), so the image decides what a name maps to (droot warns when a name other than `root` maps to uid 0); numeric IDs do not depend on the image. Unknown numeric IDs are accepted. |
| `-g`, `--group GROUP` | Group name or ID to run as, looked up like `--user` (`ROOT_DIR/etc/group`, the host's `/etc/group`, `getent group`). Defaults to the primary group of `--user`; for a numeric `--user` without an entry in `ROOT_DIR/etc/passwd`, to droot's own group (usually 0, with a warning), like Docker. |
| `-w`, `--workdir DIR` | Working directory of `COMMAND`, an absolute path inside the container. Defaults to the image's `WORKDIR`, or `/`. It must exist. |
| `-b`, `--bind SRC[:DEST][:OPTS]` | Bind mount the host's `SRC` (directory or file) on `DEST` (default: `SRC`) in the container. `OPTS` is a comma-separated list of `ro` (read-only), `rw` (the default) and `rbind` (also bind the mounts below `SRC`; with `ro`, they are all read-only), as in `--bind /srv/data:/data:ro` or `--bind /srv:rbind`. A last part that is not such a list belongs to `DEST` (`--bind /a:/b:c` mounts on `/b:c`). Repeatable. |
| `--robind SRC[:DEST][:OPTS]` | Same as `--bind SRC:DEST:ro`. Repeatable. |
| `--cp`, `--copy-files` | Copy the host's `/etc/group`, `/etc/passwd`, `/etc/resolv.conf` and `/etc/hosts` into the container. |
| `--bind-net-files` | Bind mount the host's `/etc/resolv.conf` and `/etc/hosts` read-only on the same paths in the container, so that `COMMAND` sees later changes to them (e.g. after a DHCP renewal) instead of the snapshot copied by `--cp`. Each run binds the current files afresh; note that a file bind mount follows changes made *in place*: a host file replaced with `rename(2)` while `COMMAND` runs (some DHCP clients, `resolvconf`) is only seen by the next run. With `--cp`, only `/etc/group` and `/etc/passwd` are copied. |
| `-e`, `--env KEY[=VALUE]` | Set an environment variable, overriding `/.drootenv` and `--env-file`. A bare `KEY` takes its value from droot's own environment and is left out if unset, like `docker run -e KEY` (under `sudo`, keep it with `sudo --preserve-env=KEY`). Repeatable. Commas are not separators. |
| `--env-file FILE` | Read environment variables from the host's `FILE`: `KEY=VALUE` lines (the format of `/.drootenv`; `#` starts a comment line) and bare `KEY` lines, taken from droot's environment. Overrides `/.drootenv`; later files override earlier ones. Repeatable. |
| `--no-dropcaps` | Do not drop capabilities (dangerous). |
| `--no-new-privileges` | Set the `no_new_privs` flag: setuid programs (`su`, `sudo`, ...) and file capabilities cannot grant privileges to `COMMAND`. Recommended with `--user`. |

Notes:

- Paths given to `--bind`/`--robind` and the files copied by `--cp` are resolved
  *inside* `ROOT_DIR`: symbolic links in the image cannot redirect them to the
  host's file system. A bind mount target may not be `ROOT_DIR` itself nor lie
  inside `/proc` or `/sys`.
- The supplementary groups are those listing the user in `ROOT_DIR/etc/group`.
- `COMMAND` is looked up in the `PATH` of the container environment (falling
  back to `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`), not in
  the `PATH` of the shell that started droot.
- The image's `STOPSIGNAL` is saved but not used: droot is not a supervisor.
  Set it as `KillSignal=` in a systemd unit, for instance.
- With `--cp`, the container's `/etc/passwd` and `/etc/group` are the host's,
  so the image's `USER` must exist on the host too, or be overridden with
  `--user`.

### `droot umount`

```
droot umount --root ROOT_DIR
```

Lazily unmounts every mount point below `ROOT_DIR`, deepest first, and fails
if some cannot be unmounted. `ROOT_DIR` itself is never unmounted, so it can
be a file system of its own. The command is idempotent and refuses `/`.

### Global options

| Option | Description |
|---|---|
| `-D`, `--debug` | Print debug messages to STDERR. Also enabled by `DROOT_DEBUG=1`. |
| `-v`, `--version` | Print the version. |

### Shell completion

`droot completion bash|zsh|fish` prints a completion script:

```console
$ source <(droot completion bash)     # in ~/.bashrc
$ source <(droot completion zsh)      # in ~/.zshrc, after compinit
$ droot completion fish > ~/.config/fish/completions/droot.fish
```

## Choosing the Docker daemon

`droot export` finds the daemon like the `docker` CLI does:

1. `DOCKER_HOST` (`unix:///path/to/docker.sock`, `tcp://host:2376`), with
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

`ssh://` hosts are not supported; forward the remote socket instead:
`ssh -nNT -L /tmp/docker.sock:/var/run/docker.sock host` and
`DOCKER_HOST=unix:///tmp/docker.sock`.

## Documentation

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
