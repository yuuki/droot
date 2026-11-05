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
- On a CI runner where the image was pushed by another job, add `--pull`:
  droot pulls it first with the credentials of the docker CLI.
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

[`examples/systemd/app.service`](../examples/systemd/app.service) runs the
application as a service:

```ini
[Service]
Type=exec
Environment=DROOT_LOG_FORMAT=json
Environment=APP_ENV=production
ExecStart=/usr/local/bin/droot run --root /var/containers/app/current \
    --unshare \
    --user app --cap-drop ALL --seccomp default \
    --robind /etc/app/config.yml:/app/config.yml \
    --bind /var/log/app \
    --bind-net-files \
    --tmpfs /tmp \
    --env APP_ENV \
    -- /app/bin/server --config /app/config.yml
KillSignal=SIGTERM
Restart=on-failure
MemoryMax=1G
CPUQuota=200%
TasksMax=512
LimitNOFILE=65536
```

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
  image's: `Environment=` of the unit reaches the application through
  `--env KEY` (or `--env-file FILE`).
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
# cp app.service /etc/systemd/system/
# systemctl daemon-reload
# systemctl enable --now app
# droot status --root /var/containers/app/current
```

## Rolling back

capze keeps the previous releases: point `current` back to one of them, or
deploy the previous archive again, and restart the service.
