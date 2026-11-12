# Deploying an application with droot

This tutorial follows an application from `docker build` to a systemd service
on a server, with droot in the middle:

```
build host                     storage              servers
docker build → droot export → S3 (or HTTP, ...) → verify, extract, switch → systemd: droot run
```

It assumes an image `registry.example.com/app:1.2.3` whose server is
`/app/bin/server`, run as the user `app`, reading `/app/config.yml` and writing
logs to `/var/log/app`. Adapt the names, the bucket and the paths.

## 1. Build

Build the image as usual. Everything the application needs at run time must be
in it: droot runs it on the servers without Docker.

```console
$ docker build -t registry.example.com/app:1.2.3 .
```

Declare the user, the working directory and the command in the Dockerfile:
`droot run` uses them as defaults, like `docker run` (see
[`droot run`](../README.md#droot-run)).

```dockerfile
RUN adduser -D -u 1500 app
USER app
WORKDIR /app
ENTRYPOINT ["/app/bin/server"]
CMD ["--config", "/app/config.yml"]
```

## 2. Export

Turn the image into a compressed tar archive, with its checksum:

```console
$ droot export --compress gzip --checksum --reproducible --drop-docker-files \
    -o app-1.2.3.tar.gz registry.example.com/app:1.2.3
$ cat app-1.2.3.tar.gz.sha256
3f0c…  app-1.2.3.tar.gz
```

- `--reproducible` makes the archive depend only on the image: exporting the
  same image again gives the same bytes, so the checksum identifies the
  release.
- `--drop-docker-files` leaves out `/.dockerenv` and the empty
  `/etc/hostname`, `/etc/hosts` and `/etc/resolv.conf` Docker creates in every
  container; the service below binds the host's.
- On a CI runner where the image was pushed by another job, export it
  straight from the registry, without Docker:
  `droot export ... docker://registry.example.com/app:1.2.3` (with the
  credentials of `docker login`), or pin it by digest
  (`docker://registry.example.com/app@sha256:…`). With a Docker daemon,
  `--pull` pulls it into the daemon first.
- `--exclude PATTERN` slims the archive, e.g. `--exclude /usr/share/doc`.

To sign the release, sign the checksum file, e.g. with
[cosign](https://docs.sigstore.dev/):

```console
$ cosign sign-blob --bundle app-1.2.3.tar.gz.sha256.sigstore.json app-1.2.3.tar.gz.sha256
```

## 3. Ship

Upload the archive and its checksum (and signature) anywhere the servers can
download from:

```console
$ aws s3 cp app-1.2.3.tar.gz s3://example-releases/app/
$ aws s3 cp app-1.2.3.tar.gz.sha256 s3://example-releases/app/
```

## 4. Deploy

On each server, as root: download, verify, extract into a new directory, then
switch the release atomically. [capze](https://github.com/yuuki/capze) keeps
the releases in a Capistrano-like layout, with a `current` symlink to the
active one, and removes the old ones (see its README for the details):

```console
# cd /tmp
# aws s3 cp s3://example-releases/app/app-1.2.3.tar.gz .
# aws s3 cp s3://example-releases/app/app-1.2.3.tar.gz.sha256 .
# sha256sum -c app-1.2.3.tar.gz.sha256
app-1.2.3.tar.gz: OK
# mkdir /tmp/app-1.2.3
# tar -xpzf app-1.2.3.tar.gz -C /tmp/app-1.2.3
# capze --keep 5 /tmp/app-1.2.3 /var/containers/app
# systemctl restart app
```

Extract as root with `tar -p`, so that ownership, permissions and setuid bits
are those of the image — and only extract archives you verified: they are
extracted, and later run, as root.

`droot run --root /var/containers/app/current` resolves the symlink: each run
uses the release `current` pointed to when it started.

To let the servers pull the releases instead, see
[Deploying with dewy](#deploying-with-dewy); to have them deploy the release
that a Consul or Serf event announces, see
[Deploying with stretcher](#deploying-with-stretcher).

> [!WARNING]
> Never delete a root directory that still has mounts below it: deleting it
> would delete files of the host through the bind mounts. With `--unshare`
> (below), the mounts disappear with the service. Otherwise run
> `droot umount --root DIR` first, and check with `droot status --root DIR`.

## 5. Run with systemd

The options of `droot run` go in a file,
[`examples/droot/app.toml`](../examples/droot/app.toml), installed as
`/etc/droot/app.toml`:

```toml
root = "/var/containers/app/current"
unshare = true
user = "app"
cap-drop = ["ALL"]
seccomp = "default"
robind = ["/etc/app/config.yml:/app/config.yml"]
bind = ["/var/log/app"]
bind-net-files = true
tmpfs = ["/tmp"]
env = ["APP_ENV"]
command = ["/app/bin/server", "--config", "/app/config.yml"]
```

so that [`examples/systemd/app.service`](../examples/systemd/app.service)
stays short:

```ini
[Service]
Type=exec
Environment=DROOT_LOG_FORMAT=json
Environment=APP_ENV=production
ExecStart=/usr/local/bin/droot run --config /etc/droot/app.toml
KillSignal=SIGTERM
Restart=on-failure
MemoryMax=1G
CPUQuota=200%
TasksMax=512
LimitNOFILE=65536
```

Options given on the command line take precedence over the file, e.g.
`droot run --config /etc/droot/app.toml --user root -- sh` to debug.

- `--unshare` makes the mounts in a private mount namespace: they disappear
  when the service stops, and old releases can be deleted safely.
- `--user app` (the image's `USER` would be the default anyway),
  `--cap-drop ALL` and `--seccomp default` (a seccomp filter like Docker's
  default profile) limit what the application can do, as does
  `no_new_privs`, set by default (setuid programs cannot gain privileges);
  see [Security considerations](security.md).
- `--robind` and `--bind` give the application its configuration
  (read-only) and its log directory; `--bind-net-files` the host's
  `/etc/resolv.conf` and `/etc/hosts`; `--tmpfs /tmp` a private `/tmp`.
- droot does not pass its own environment to the application, only the
  image's and the variables of systemd's protocols (see
  [below](#6-notifications-and-socket-activation)): `Environment=` of the
  unit reaches the application through `--env KEY` (or `--env-file FILE`).
- droot *replaces itself* with the application, so the main process of the
  service is the application: signals, the exit status and `Restart=` work
  as with any service. Set `KillSignal=` to the `STOPSIGNAL` of the image, if
  it has one (`droot export` saves it in `/.droot/config.json`).
- droot creates no cgroups: systemd's `MemoryMax=`, `CPUQuota=`, `TasksMax=`
  and `Limit*=` limit the service.
- With `DROOT_LOG_FORMAT=json`, the messages of droot are JSON objects in the
  journal (`journalctl -u app`).

If you do not want a private mount namespace, use
[`examples/systemd/app-host-mounts.service`](../examples/systemd/app-host-mounts.service)
instead: it removes the mounts with `droot umount` in `ExecStopPost=`, and in
`ExecStartPre=` in case a crash left some behind.

```console
# mkdir -p /etc/droot && cp app.toml /etc/droot/
# cp app.service /etc/systemd/system/
# systemctl daemon-reload
# systemctl enable --now app
# droot status --root /var/containers/app/current
```

## 6. Notifications and socket activation

droot passes on to the application what systemd gives the main process of
a service for its two protocols, so that a service run with droot keeps
the units it would have without it:

- **`Type=notify`**: `NOTIFY_SOCKET`, the socket the application sends
  `READY=1`, `STATUS=...` or `WATCHDOG=1` to
  ([`sd_notify(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_notify.html)).
  droot bind mounts it read-only on the same path in the container
  (`/run/systemd/notify`). With `WatchdogSec=`, `WATCHDOG_USEC` and
  `WATCHDOG_PID` too.
- **Socket activation**: the sockets of a `.socket` unit, which the
  application inherits as descriptors 3, 4, ..., with `LISTEN_FDS`,
  `LISTEN_PID` and `LISTEN_FDNAMES`
  ([`sd_listen_fds(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_listen_fds.html)).

`--no-systemd` (`no-systemd = true` in the `--config` file) turns this off
for a container that must have no channel to the service manager: droot
then passes none of these variables and binds no socket.

[`examples/systemd/app-notify.service`](../examples/systemd/app-notify.service)
is `app.service` for an application that notifies systemd:

```ini
[Service]
Type=notify
#WatchdogSec=30s
Environment=DROOT_LOG_FORMAT=json
Environment=APP_ENV=production
ExecStart=/usr/local/bin/droot run --config /etc/droot/app.toml
```

[`examples/systemd/app.socket`](../examples/systemd/app.socket) makes
systemd listen on port 8080 and start `app.service` at the first
connection. The service needs no change, but the socket now starts it:
stop and disable the service (systemd does not start a socket whose service
runs already, and an enabled service would start at boot, without waiting
for a connection), and enable the socket instead.

```ini
[Socket]
ListenStream=8080
FileDescriptorName=http

[Install]
WantedBy=sockets.target
```

```console
# cp app.socket /etc/systemd/system/
# systemctl daemon-reload
# systemctl disable --now app
# systemctl enable --now app.socket
```

The application must use the socket it is given instead of opening its own:
with `sd_listen_fds(3)`, or a library doing the same, e.g.
`activation.Listeners()` of [go-systemd](https://github.com/coreos/go-systemd).
The socket is opened by systemd, as root: `ListenStream=80` needs no
`CAP_NET_BIND_SERVICE` in the container.

As root, droot replaces itself with the application, which keeps the PID
of droot: for systemd, the application is the main process of the service,
the default `NotifyAccess=main` accepts its notifications, and `LISTEN_PID`
and `WATCHDOG_PID` are its PID.

Rootless (a unit with `User=`, or a user service), droot stays the main
process and supervises the application, its child. droot passes the
sockets on to it and makes `LISTEN_PID` and `WATCHDOG_PID` its PID, and
relays its notifications: the application gets a socket of droot in place
of systemd's, and droot forwards what the application itself sends (not
its children, and without `MAINPID=`), from the main process, which the
default `NotifyAccess=main` accepts:

```ini
[Service]
Type=notify
KillMode=mixed
```

An application whose notifications come from a child (a shell script
running `systemd-notify --ready`, an init such as `tini`) should `exec` the
program that notifies; or else bind systemd's socket in the container with
`--robind $NOTIFY_SOCKET` and set `NotifyAccess=all`. droot creates its
socket in `$XDG_RUNTIME_DIR` (a user service), `$RUNTIME_DIRECTORY` or
`/tmp`: a hardened unit whose `/tmp` is read-only (`ProtectSystem=strict`
without `PrivateTmp=`) needs `RuntimeDirectory=`, or else the
notifications are not relayed (droot warns) and need `NotifyAccess=all`.

## Rolling back

capze keeps the previous releases: point `current` back to one of them, or
deploy the previous archive again, and restart the service.

## Deploying with dewy

Instead of pushing the releases with capze, the servers can pull them with
[dewy](https://github.com/linyows/dewy). `dewy server` polls a registry
(GitHub Releases, Amazon S3, Google Cloud Storage, ...) and, for each new
version (by semantic versioning), downloads it, extracts it into
`releases/TIMESTAMP/`, switches the `current` symlink and restarts the server
without dropping connections when the new release starts: like server-starter,
dewy holds the listening socket and passes it on to each new process. It
checks the new release with an HTTP request and rolls back if the check fails.

dewy's own extraction does not suit a root file system: it refuses symbolic
links, which every distribution has by the hundred, and drops the owners and
the setuid, setgid and sticky bits. The artifact of a release therefore
carries the droot archive, which a hook of dewy extracts with `tar`.

### Publishing a release

dewy picks the artifact whose name contains the OS and the architecture of the
server (`app_linux_amd64.tar.gz`), and verifies it against a checksum file
published next to it:

```console
$ mkdir -p build
$ droot export --compress gzip --checksum -o build/rootfs.tar.gz registry.example.com/app:1.2.3
$ tar -czf app_linux_amd64.tar.gz -C build rootfs.tar.gz rootfs.tar.gz.sha256
$ sha256sum app_linux_amd64.tar.gz > app_linux_amd64.tar.gz.sha256
$ gh release create v1.2.3 app_linux_amd64.tar.gz app_linux_amd64.tar.gz.sha256
```

With S3 or GCS, upload the two files to `PREFIX/v1.2.3/`. dewy refuses
artifacts larger than 512 MiB, and downloads them in memory, which takes
several times their size.

### Running dewy

On each server, as root, install the hook
[`examples/dewy/droot-extract-release`](../examples/dewy/droot-extract-release)
as `/usr/local/bin/droot-extract-release`, and the unit
[`examples/dewy/app.service`](../examples/dewy/app.service):

```ini
[Service]
Type=exec
WorkingDirectory=/var/containers/app
EnvironmentFile=/etc/dewy/app.env
ExecStart=/usr/local/bin/dewy server \
    --registry ghr://example/app \
    --port 8080 --health-path /health \
    --after-deploy-hook /usr/local/bin/droot-extract-release \
    --log-format json --log-level info \
    -- /usr/local/bin/droot run --unshare \
        --env SERVER_STARTER_PORT --env SERVER_STARTER_GENERATION \
        --user app --cap-drop ALL --seccomp default \
        --bind /var/log/app --bind-net-files --tmpfs /tmp \
        --root /var/containers/app/current/rootfs
```

```console
# mkdir -p /var/containers/app /etc/dewy
# install -d -o 1500 -g 1500 /var/log/app      # owned by the image's user app (uid 1500 here)
# install -m 600 /dev/null /etc/dewy/app.env   # then add GITHUB_TOKEN=..., or the AWS_* variables
# cp app.service /etc/systemd/system/
# systemctl daemon-reload
# systemctl enable --now app
```

For each new version, dewy:

1. downloads the artifact, verifies its checksum, and extracts it into
   `releases/TIMESTAMP/`: `rootfs.tar.gz` and its checksum;
2. points `current` to the release and runs the hook, which verifies
   `rootfs.tar.gz`, extracts it into `releases/TIMESTAMP/rootfs` as root with
   `tar -p` (owners, modes, setuid bits and links as in the image) and deletes
   it;
3. starts a new `droot run` on the listening socket, stops the previous one
   with `SIGTERM` right away, without waiting for the new one to answer, and
   requests `/health` on port 8080; if no request succeeds within 30 seconds
   (`--health-timeout`), it points `current` back to the previous release,
   restarts it, and skips that version from then on;
4. once the new release is healthy, deletes the releases beyond the 7 most
   recent (by modification time).

Notes:

- `--unshare` is required. dewy deletes the old releases with all their files:
  without a private mount namespace, the mounts that `droot run` leaves in a
  release (`/proc`, `/sys`, the `--bind` mounts) would still be there, and
  the deletion would delete files of the host through the bind mounts — again
  at every deployment, since it then fails on `/proc` and leaves the release.
- `--env SERVER_STARTER_PORT` is required. droot passes the listening socket
  of dewy on to the application (as descriptor 3) but not the environment of
  dewy, where `SERVER_STARTER_PORT=8080=3` gives its number. The application
  must use that socket, e.g. with
  [`github.com/lestrrat-go/server-starter/listener`](https://pkg.go.dev/github.com/lestrrat-go/server-starter/listener)
  in Go: it cannot listen on the port itself, which is dewy's. The socket being
  open already, the application needs no privilege for a port below 1024.
- `droot run --root /var/containers/app/current/rootfs` resolves the symlink
  when it starts, so each process stays in its release.
- Keep `--health-path`. dewy goes on when the hook fails (a full disk, a
  corrupt archive): it restarts the server anyway, the previous process is
  stopped, and the new `droot run` fails, since the release has no `rootfs`,
  and is started again and again without delay (filling the log) until the
  health check gives up: the service is down for `--health-timeout`. dewy
  then rolls back and skips that version for good: to deploy it again once
  the cause is fixed, delete `.dewy/cache/blocked`, or publish a new version.
  With nothing to roll back to (the first deployment), or without a health
  check, the service stays down.
- The application must stop gracefully on `SIGTERM`, which dewy sends to the
  previous process at every deployment and cannot change: the `STOPSIGNAL`
  of the image does not apply.
- The default `KillMode=control-group` of the unit sends `SIGTERM` to the
  application as well as to dewy, so that the application stops even if dewy
  exits before passing the signal on.
- dewy keeps every artifact it downloaded in its cache (`.dewy/cache` in its
  working directory, or `$DEWY_CACHEDIR`): delete the files of old versions
  (`vX.Y.Z--NAME`) from time to time, but not `current`, `blocked` or the
  file of the running version, which would make dewy deploy again.
- The limits of the unit (`MemoryMax=`, ...) cover dewy, the hook and, during
  a deployment, two processes of the application.
- Run as a user other than root, dewy runs droot rootless, which passes the
  socket on as well. The hook, run by dewy, then extracts the files as that
  user, who is root in the user namespace of droot: run the application with
  `--user 0`.

## Deploying with stretcher

[stretcher](https://github.com/fujiwara/stretcher) is an agent that deploys,
on each server, the release a manifest describes. A Consul or Serf event
gives it the URL of the manifest, or its standard input does (over `ssh`,
for instance). It downloads the archive (Amazon S3, Google Cloud Storage,
HTTP(S) or a local file), verifies its checksum, extracts it with `tar` into
a temporary directory, moves the files to the `dest` of the manifest, and
runs the commands of the manifest before and after. Run as root, its `tar`
keeps the owners, modes, setuid bits and links of the image, like `tar -xp`
in [4. Deploy](#4-deploy).

By default, stretcher synchronizes `dest` with `rsync -a --delete`, which
does not suit a root directory of droot: never let it update a root that
`droot run` uses. rsync deletes what droot creates in the root and the
archive does not contain: the device nodes of `/dev`, and the mount points
of `/dev/pts`, `/dev/shm` and of the `--bind`, `--tmpfs` and
`--bind-net-files` mounts. With `--unshare`, a running application loses
them (its `/dev/null`, its bind mounts) while stretcher reports a success.
Without `--unshare`, rsync deletes files of the host through the bind mounts
left in the root (those before `/proc` in alphabetical order), then fails
on `/proc`. Instead, have stretcher extract
each release into a new directory and switch a `current` symlink, as capze
does.

### Publishing a release

Export the image as in [2. Export](#2-export), generate the manifest of the
release from the template
[`examples/stretcher/deploy.yml`](../examples/stretcher/deploy.yml), upload
both, and send the event:

```console
$ droot export --compress gzip --checksum --reproducible --drop-docker-files \
    -o app-1.2.3.tar.gz registry.example.com/app:1.2.3
$ sed -e 's/@VERSION@/1.2.3/g' -e "s/@SHA256@/$(cut -d' ' -f1 app-1.2.3.tar.gz.sha256)/" \
    deploy.yml >deploy-1.2.3.yml
$ aws s3 cp app-1.2.3.tar.gz s3://example-releases/app/
$ aws s3 cp deploy-1.2.3.yml s3://example-releases/app/
$ consul event -name deploy-app s3://example-releases/app/deploy-1.2.3.yml
```

The manifest of the release, without the comments of the template:

```yaml
src: s3://example-releases/app/app-1.2.3.tar.gz
checksum: "3f0c…"
dest: /var/containers/app/releases/1.2.3
sync_strategy: mv

commands:
  pre:
    - mkdir -p /var/containers/app/releases
  post:
    - ln -sfn releases/1.2.3 /var/containers/app/current.new && mv -T /var/containers/app/current.new /var/containers/app/current
    - systemctl restart app
    - cd /var/containers/app/releases && ls -1t | tail -n +6 | while read -r r; do /usr/local/bin/droot umount --root "$r" && rm -rf -- "$r"; done
```

### Running stretcher

On each server, set up the service as in [5. Run with systemd](#5-run-with-systemd):
`/etc/droot/app.toml` runs the release `current` points to
(`root = "/var/containers/app/current"`) in a private mount namespace
(`unshare = true`). Create the directory stretcher extracts the archives
into, on the file system of the releases, and run stretcher as root with it
as `TMPDIR`, e.g. under `consul watch` (in a unit of its own, with
`Environment=TMPDIR=/var/containers/app/.tmp`):

```console
# install -d -m 700 /var/containers/app/.tmp
# TMPDIR=/var/containers/app/.tmp consul watch -type event -name deploy-app /usr/local/bin/stretcher
```

For each event, stretcher:

1. downloads the manifest, then the archive into `$TMPDIR`, and verifies its
   checksum;
2. runs the `pre` commands, which create `releases/`;
3. extracts the archive with `tar` into a new directory of `$TMPDIR`, as
   root, and renames it to `releases/1.2.3` (`sync_strategy: mv`);
4. runs the `post` commands: points `current` to the release (`mv -T`
   replaces the symlink atomically), restarts the service, and deletes the
   releases beyond the 5 most recently modified, after removing with
   `droot umount` the mounts that a run without `--unshare` may have left in
   them;
5. runs the `success` commands, or, after the first step that failed, the
   `failure` commands, with its log on their standard input.

Notes:

- `$TMPDIR` must be on the file system of the releases: `sync_strategy: mv`
  renames the extracted directory, which fails across file systems
  ("invalid cross-device link"). By default, it is `/tmp`, often a tmpfs,
  which would also hold the whole image in memory. It needs room for the
  archive and the extracted files.
- `mv` refuses a release directory that exists ("file exists"): each
  version is deployed once. A manifest delivered again (an event replayed)
  fails and changes nothing. To deploy a version again, delete its directory
  first, if `current` does not point to it, or give the release another
  name.
- Prefer `mv` to `rsync`, also with a new directory as `dest`: rsync works
  too, but turns the hard links of the image into copies (`rsync -a`
  without `-H`).
- The owners, modes and setuid bits are those of the image only if
  stretcher runs as root. GNU `tar` recognizes gzip archives, and zstd ones
  (`droot export --compress zstd`) if the `zstd` command is installed.
- If the restart fails, stretcher stops: `current` points to the new
  release, and no release is deleted. To roll back, point `current` to the
  previous release with the same `ln -sfn` and `mv -T`, and restart the
  service; deploying the manifest of the previous release again fails, its
  directory existing.
- With `--unshare`, the mounts of a release disappear with the service, so
  that deleting old releases is safe. The `droot umount` before `rm -rf`
  also covers releases that were run without it.
- The commands of a manifest run as root on every server, and the checksum
  that protects the archive comes from the manifest itself: whoever can
  write a manifest, or send the event, controls the servers. Serve the
  manifests from S3 or over HTTPS, with restricted write access, and
  restrict `consul event` with the ACLs of Consul. See
  [Security considerations](security.md).
