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
sockets on to it and makes `LISTEN_PID` and `WATCHDOG_PID` its PID, but
systemd only accepts notifications from other processes than the main one
with `NotifyAccess=all`:

```ini
[Service]
Type=notify
NotifyAccess=all
KillMode=mixed
```

## Rolling back

capze keeps the previous releases: point `current` back to one of them, or
deploy the previous archive again, and restart the service.
