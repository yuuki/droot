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
$ make fuzz               # run every fuzz target for FUZZTIME (default 30s)
$ make docs               # regenerate documentation/cli.md and droot.1 (a test checks them)
$ make lint               # golangci-lint (configuration in .golangci.yml)
$ make fmt                # gofmt + goimports
$ make vet                # go vet for Linux and macOS
$ make vuln               # govulncheck: known vulnerabilities reachable from droot
$ make cross              # build for every released platform
$ make snapshot           # build the release assets into dist/ without publishing or signing
$ make test-scripts       # test the release scripts
$ make release-prep VERSION=x.y.z   # move the Unreleased changes to a new version
```

`make lint`, `make fmt`, `make vuln` and `make snapshot` run pinned versions
of golangci-lint, govulncheck and GoReleaser with `go run`. These tools may
require a newer Go than droot itself (GoReleaser v2.20.0 needs Go 1.27.3):
keep the default `GOTOOLCHAIN=auto` so that the go command downloads it, or
install the tools separately when `GOTOOLCHAIN=local` is enforced (e.g. a
distribution's Go). `make snapshot` also makes the SBOMs when
[syft](https://github.com/anchore/syft) is installed.

## Tests

| Level | Where | Needs |
|---|---|---|
| Unit | `*_test.go` | nothing; Docker is faked (`internal/docker/export_test.go`) or served by `httptest` (`internal/docker/client_test.go`) |
| Privileged unit | `internal/mounter/mount_linux_test.go`, `internal/osutil/sys_linux_test.go` | root on Linux; skipped otherwise |
| Fuzz | `fuzz_test.go` of `environ` (`/.drootenv` parsing), `mounter` (`/proc/self/mountinfo` parsing), `osutil` (path resolution of `osutil.Root`, Linux) and `docker` (rewriting of the exported archive) | nothing; `make fuzz` explores for `FUZZTIME` (30s) per target, their seeds and `testdata/fuzz` run with the unit tests |
| End-to-end | `script/integration-test.sh` | root, Docker (or Podman: `DOCKER=podman DOCKER_HOST=unix:///run/podman/podman.sock`), network access to pull `alpine` and `busybox` |

The privileged mount tests create their scratch directory with
`os.MkdirTemp` and only delete it when nothing is mounted below it any more,
so that a failing unmount can never make the cleanup delete files through a
bind mount.

CI (`.github/workflows/ci.yml`) runs on every push and pull request:

- `lint`: golangci-lint, `go mod tidy -diff` and `make test-scripts`;
- `test`: the unit tests with the minimum and the latest supported Go
  versions, unprivileged and as root;
- `fuzz`: `make fuzz`, every fuzz target for 30 seconds;
- `coverage`: the unit tests as root with a coverage report, shown in the
  job summary and kept as the `coverage` artifact;
- `integration`: the end-to-end tests on amd64 and arm64 runners against
  Docker, and on amd64 against Podman's Docker-compatible API;
- `vulnerabilities`: govulncheck with the Go version releases are built
  with, so that a vulnerable dependency or standard library fails CI;
- `workflows`: [zizmor](https://docs.zizmor.sh), a security linter for
  GitHub Actions workflows (template injection, excessive permissions,
  unpinned actions, cache poisoning, ...);
- `release snapshot`: the release assets built exactly like a release (but
  not signed), kept for 7 days as the `snapshot` artifact of the run, and a
  preview of the release notes in the run summary.

## Dependencies

droot intentionally depends on as little as possible: only
`golang.org/x/sys`. The command line is parsed by a small package
(`internal/cli`), and Docker is spoken to with the standard library
(`internal/docker`) instead of the Docker SDK, which pulls in dozens of
modules. Please keep it that way; update `LICENSE_OF_DEPENDENCIES.md` when
adding a dependency.

Dependabot (`.github/dependabot.yml`) proposes monthly, grouped updates of Go
modules and GitHub Actions, once they have been released for a week. The
workflows pin every action to a full commit SHA, with its version in a
comment; Dependabot updates both. The versions of golangci-lint,
govulncheck, GoReleaser, syft and zizmor are pinned in the `Makefile` and
the workflows and are updated by hand.

## Releasing

droot follows [semantic versioning](https://semver.org). `CHANGELOG.md` is
the source of the release notes: every pull request with a user-visible
change adds an entry under "Unreleased".

1. Prepare the changelog in a pull request:

   ```console
   $ git switch -c release-0.8.0 origin/master
   $ make release-prep VERSION=0.8.0   # "Unreleased" becomes "[0.8.0] - YYYY-MM-DD"
   $ git commit -am "release: v0.8.0"
   ```

   Review the section (the introduction, the breaking changes) and the
   `release snapshot` job of the pull request, which builds the assets and
   shows the release notes in its summary.
2. Once it is merged, tag the merge commit and push the tag:

   ```console
   $ git switch master && git pull --ff-only
   $ git tag -s v0.8.0 -m v0.8.0 && git push origin v0.8.0
   ```

3. The `Release` workflow (`.github/workflows/release.yml`):
   1. checks that the tag is a semantic version, that `CHANGELOG.md` has a
      dated section for it and that the tagged commit is on `master`;
   2. runs the whole CI on the tagged commit;
   3. builds the assets with GoReleaser (`.goreleaser.yaml`), with the latest
      stable Go and without any cache, and an SPDX SBOM of each archive
      with syft;
   4. signs `checksums.txt` with [cosign](https://docs.sigstore.dev/cosign/)
      keyless signing: the Sigstore certificate in
      `checksums.txt.sigstore.json` names the workflow and the tag;
   5. creates a **draft** GitHub release whose notes are the changelog
      section followed by instructions to verify the downloads;
   6. creates a signed [SLSA build provenance attestation](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations/using-artifact-attestations-to-establish-provenance-for-builds)
      of every asset listed in `checksums.txt` and of `checksums.txt`
      itself (keyless signature by
      [Sigstore](https://www.sigstore.dev), listed under the repository's
      *Attestations*).
4. Review the draft and publish it.
5. Update the formula of the Homebrew tap,
   [`yuuki/homebrew-droot`](https://github.com/yuuki/homebrew-droot):
   `script/homebrew-formula.sh` writes it from the `checksums.txt` of the
   release, once verified.

   ```console
   $ gh release download v0.8.0 --repo yuuki/droot --dir /tmp/droot-v0.8.0 \
       --pattern checksums.txt --pattern checksums.txt.sigstore.json
   $ cosign verify-blob /tmp/droot-v0.8.0/checksums.txt --bundle /tmp/droot-v0.8.0/checksums.txt.sigstore.json \
       --certificate-identity https://github.com/yuuki/droot/.github/workflows/release.yml@refs/tags/v0.8.0 \
       --certificate-oidc-issuer https://token.actions.githubusercontent.com
   $ script/homebrew-formula.sh v0.8.0 /tmp/droot-v0.8.0/checksums.txt >../homebrew-droot/droot.rb
   $ cd ../homebrew-droot && git commit -am "droot 0.8.0" && git push
   $ brew update && brew upgrade droot && brew test droot
   ```

   The formula installs the archive of macOS or Linux on amd64 or arm64,
   with the man page; `brew install --HEAD droot` builds `master` with Go.

If the workflow fails before the release is published, fix the cause and
re-run it: the draft is replaced. Never move a published tag; release a new
patch version instead.

**Pre-releases**: push a tag such as `v0.8.0-rc.1` without preparing the
changelog. The release is marked as a pre-release and its notes are the
"Unreleased" section.

### Assets

The assets keep the names and layout of droot 0.7.7, so that download
scripts using `releases/latest/download/...` keep working. `<arch>` is
`amd64`, `arm64` or, for Linux only, `armv7` (32-bit ARM):

| Asset | Content |
|---|---|
| `droot_linux_<arch>.tar.gz`, `droot_darwin_<arch>.zip` | `droot_<os>_<arch>/droot`, the licenses, `README.md`, `CHANGELOG.md`, the man page `droot.1` |
| `droot_<os>_<arch>` | the bare binary |
| `<archive>.sbom.json` | an SPDX SBOM of the archive (Go modules and Go version) |
| `checksums.txt` | the SHA-256 of every asset above |
| `checksums.txt.sigstore.json` | the cosign signature of `checksums.txt` (a Sigstore bundle) |

### Verifying a release

```console
$ gh attestation verify droot_linux_amd64 --repo yuuki/droot \
    --signer-workflow yuuki/droot/.github/workflows/release.yml --source-ref refs/tags/v0.8.0
```

checks that the file was built from the tag by the release workflow of this
repository. Verify `checksums.txt` the same way to check many files with
`sha256sum --ignore-missing -c checksums.txt`.

`gh attestation verify` needs a GitHub account. Without one, check the cosign
signature of `checksums.txt`, which was made by the same workflow run:

```console
$ cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
    --certificate-identity https://github.com/yuuki/droot/.github/workflows/release.yml@refs/tags/v0.8.0 \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
$ sha256sum --ignore-missing -c checksums.txt
```

### Reproducing a release

The binaries and the archives are reproducible: they contain no local path
or build time (file times are those of the tagged commit), so anyone can
rebuild them and compare the checksums. The Go version used is recorded in
the binary:

```console
$ go version -m droot_linux_amd64 | head -n 1
droot_linux_amd64: go1.27.3
$ git clone --branch v0.8.0 https://github.com/yuuki/droot && cd droot
$ GOTOOLCHAIN=go1.27.3 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-s -w -X main.version=0.8.0 -X main.commit=$(git rev-parse HEAD) -X main.date=$(date -u -d @$(git log -1 --format=%ct) +%Y-%m-%dT%H:%M:%SZ)" \
    -o droot_linux_amd64 ./cmd/droot
$ sha256sum droot_linux_amd64   # the same as in checksums.txt
```

`GOTOOLCHAIN=go1.27.3 go run github.com/goreleaser/goreleaser/v2@v2.20.0
release --clean --skip=publish,sbom,sign` in the clean checkout of the tag
rebuilds all the archives and binaries. (The SBOMs contain a timestamp and
a random identifier.)

### Repository settings

These settings are not stored in the repository; set them once:

- *Settings → General → Releases*: enable **immutable releases**, so that
  the assets and the tag of a published release can no longer be changed.
- *Settings → Rules → Rulesets*: a tag ruleset on `v*` that only lets the
  maintainers create, update or delete release tags.
- *Settings → Environments → release*: limit the environment to the tags
  `v*` and, optionally, require a reviewer before the release job runs.
- *Settings → Security → Private vulnerability reporting*: enable it, so
  that vulnerabilities can be reported privately as `SECURITY.md` asks.

### Version

The version printed by `droot --version` comes from `-X main.version` (set by
GoReleaser and `make build`) or, for `go install`, from the module version;
the commit and its date from `-X main.commit` and `-X main.date` (set by
GoReleaser) or from the VCS information Go embeds when building from a git
checkout.

`documentation/cli.md` and `documentation/droot.1` are generated from the
command definitions (`internal/commands`) by `make docs`; a unit test fails
when they are out of date.
