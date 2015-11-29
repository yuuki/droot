droot
=====

Droot is a super-easy container chrooting a docker image without docker.

## Overview

[Docker](https://www.docker.com) has a powerful concept about an application deployment process, that is Build, Ship, Run. But there are many cases that docker runtime is beyond our current capabilities.

`droot` provides a simpler container runtime without annoying Linux Namespace by chroot(2), Linux capabilities(7) and bind mount. `droot` helps you to chroot a container image built by `docker` and to import/export container images on Amazon S3.

![droot concept](http://cdn-ak.f.st-hatena.com/images/fotolife/y/y_uuki/20151129/20151129193210.png?1448793174)

## Requirements

- Docker (`droot push` only depends on it)
- Linux (`droot run` and `droot umount` only supports it)

## Installation

## Usage

```bash
$ droot push --to s3://example.com/dockerfiles/app.tar.gz dockerfiles/app
```

```bash
$ droot pull --dest /var/containers/app --src s3://example.com/dockerfiles/app.tar.gz
```

```bash
$ sudo droot run --bind /var/log/ --root /var/containers/app command
```

```bash
$ sudo droot umount --root /var/containers/app
```

## Roodmap

- `rm` command for cleaning container environment
- `rmi` command for cleaning image on S3
- `pull` command with the rsync option
- `push/pull` other compression algorithms
- image versioning
- `pull` from docker registry
- drivers except Amazon S3
- `run` reads `.docekrenv`, `.dockerinit`
- reduce fork&exec

## Contribution

1. Fork ([https://github.com/yuuki1/droot/fork](https://github.com/yuuki1/droot/fork))
1. Create a feature branch
1. Commit your changes
1. Rebase your local changes against the master branch
1. Run test suite with the `make test` command and confirm that it passes
1. Create a new Pull Request

## Author

[y_uuki](https://github.com/yuuki1)
