# Development guide

## Prerequisites

- Go 1.26 or later (the `go` line of `go.mod`; with the default
  `GOTOOLCHAIN=auto`, older Go installations download it automatically).
  CI tests the minimum and the latest Go release; releases are built with the
  latest stable Go.
- Linux to run `droot run`/`droot umount` and the tests that mount file
  systems. On macOS, use a Linux VM or a privileged container (`make` uses
  `sudo` only when not already root):
  `docker run --rm --privileged -v "$PWD":/src -w /src golang:1.27 make test-root`.
- Docker to run the end-to-end tests.

## Common tasks

```console
$ make help               # list the targets
$ make build              # static ./droot (CGO_ENABLED=0), version from git describe
$ make test               # unit tests with the race detector; root-only tests are skipped
$ make test-root          # unit tests as root, including real mounts and mknod (sudo)
$ make integration-test   # build and run script/integration-test.sh against Docker (sudo)
$ make lint               # golangci-lint (configuration in .golangci.yml)
$ make fmt                # gofmt + goimports
$ make vet                # go vet for Linux and macOS
$ make cross              # build for every released platform
$ make snapshot           # build release archives into dist/ without publishing
```

`make lint`, `make fmt` and `make snapshot` run pinned versions of
golangci-lint and GoReleaser with `go run`. These tools may require a newer
Go than droot itself (GoReleaser v2.20.0 needs Go 1.27.3): keep the default
`GOTOOLCHAIN=auto` so that the go command downloads it, or install the tools
separately when `GOTOOLCHAIN=local` is enforced (e.g. a distribution's Go).

## Tests

| Level | Where | Needs |
|---|---|---|
| Unit | `*_test.go` | nothing; Docker is faked (`internal/docker/export_test.go`) or served by `httptest` (`internal/docker/client_test.go`) |
| Privileged unit | `internal/mounter/mount_linux_test.go`, `internal/osutil/sys_linux_test.go` | root on Linux; skipped otherwise |
| End-to-end | `script/integration-test.sh` | root, Docker, network access to pull `alpine` and `busybox` |

The privileged mount tests create their scratch directory with
`os.MkdirTemp` and only delete it when nothing is mounted below it any more,
so that a failing unmount can never make the cleanup delete files through a
bind mount.

CI (`.github/workflows/ci.yml`) runs golangci-lint, the unit tests with the
minimum and the latest supported Go versions (unprivileged and as root), the
end-to-end tests and cross builds on every push and pull request.

## Dependencies

droot intentionally depends on as little as possible: only
`github.com/urfave/cli/v3` and `golang.org/x/sys`. Talking to Docker is done
with the standard library (`internal/docker` package) instead of the Docker SDK, which
pulls in dozens of modules. Please keep it that way; update
`LICENSE_OF_DEPENDENCIES.md` when adding a dependency.

Dependabot (`.github/dependabot.yml`) proposes monthly, grouped updates of Go
modules and GitHub Actions.

## Releasing

Releases are built by [GoReleaser](https://goreleaser.com) from
`.goreleaser.yaml` when a tag is pushed:

1. Move the entries under "Unreleased" in `CHANGELOG.md` to a new version
   section and commit.
2. Tag and push: `git tag v0.8.0 && git push origin v0.8.0`.
3. The `Release` workflow creates a **draft** GitHub release with the assets
   named like those of droot 0.7.7 — `droot_linux_<arch>.tar.gz`,
   `droot_darwin_<arch>.zip` and bare `droot_<os>_<arch>` binaries — and
   `checksums.txt`. Review it and publish it.
4. Update the Homebrew tap (`yuuki/homebrew-droot`) with the new URL and
   checksum.

The version printed by `droot --version` comes from `-X main.version` (set by
GoReleaser and `make build`) or, for `go install`, from the module version.
