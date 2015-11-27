dochroot
========

Dochroot is a CLI tool for chrooting a docker image.

## Overview

[Docker](https://www.docker.com) has a powerfull concept about application deployment process, that is Build, Ship, Run. But there are many cases that docker runtime is beyond our current capabilities. I supporse simpler container runtime by chrooting into docker image. `dochroot` helps you chrooting an image built by `docker build` command and managing exported images on Amazon S3 or a local filesystem.

## Requirements

- Docker (`dochroot push` only depends on it)

## Installation

## Usage

```bash
$ dochroot push --to s3://example.com/dockerfiles/app.tar.gz dockerfiles/app
```

```bash
$ dochroot pull --dest /var/containers/app --src s3://example.com/dockerfiles/app.tar.gz
```

```bash
# dochroot run --bind /var/log/ --root /var/containers/app -- command
```

```bash
# dochroot rmi --root /var/containers/app
```

## Roodmap

- `pull` command with the rsync option
- `push/pull` other compression algorithms
- image versioning
- `pull` from docker registry
- drivers except Amazon S3

