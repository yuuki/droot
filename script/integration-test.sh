#!/usr/bin/env bash
#
# End-to-end test of droot against a real Docker daemon.
#
# Requirements: Linux, root privileges, a running Docker daemon that can pull
# alpine and busybox. Usage:
#
#   make build && sudo ./script/integration-test.sh [path/to/droot]
#
# The work directory is not deleted (it may contain mount points if something
# goes wrong); its path is printed at the end.
set -euo pipefail

DROOT=$(realpath "${1:-./droot}")
WORK=$(mktemp -d "${TMPDIR:-/tmp}/droot-e2e.XXXXXX")
WORK=$(realpath "$WORK")
IMAGE=droot-e2e:latest
SCRATCH_IMAGE=droot-e2e-scratch:latest

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_eq() { # name actual expected
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
  echo "ok - $1"
}
mounts_under() { awk -v p="$1" '$5 == p || index($5, p "/") == 1' /proc/self/mountinfo | wc -l; }

cleanup() {
  local d
  for d in "$WORK"/*/; do
    "$DROOT" umount --root "$d" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

[[ $(id -u) == 0 ]] || fail "must be run as root"
[[ -x "$DROOT" ]] || fail "droot binary not found at $DROOT (run 'make build')"

echo "# building test images"
docker build -q -t "$IMAGE" - >/dev/null <<'EOF'
FROM alpine:3.22
RUN addgroup -g 1600 appgroup && adduser -D -u 1500 -G appgroup app \
 && addgroup -g 1700 extra && addgroup app extra \
 && mkdir -p /usr/local/myapp/bin \
 && printf '#!/bin/sh\necho myapp-ok\n' > /usr/local/myapp/bin/myapp \
 && chmod 755 /usr/local/myapp/bin/myapp
ENV PATH=/usr/local/myapp/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    JAVA_OPTS="-Dfoo=bar -Dx=y"
USER app
ENTRYPOINT ["/bin/false"]
EOF
docker build -q -t "$SCRATCH_IMAGE" - >/dev/null <<'EOF'
FROM busybox:1.37-musl AS bb
FROM scratch
COPY --from=bb /bin/busybox /busybox
ENV GREETING="no shell here"
EOF

echo "# export"
"$DROOT" export -o "$WORK/app.tar" "$IMAGE"
assert_eq "export writes .drootenv from the image config" \
  "$(tar -xOf "$WORK/app.tar" .drootenv)" \
  "$(printf 'PATH=/usr/local/myapp/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nJAVA_OPTS=-Dfoo=bar -Dx=y')"
assert_eq "export leaves no temporary container" \
  "$(docker ps -aq --filter label=io.github.yuuki.droot | wc -l)" "0"
if "$DROOT" export does-not-exist:nope >/dev/null 2>&1; then fail "exporting a missing image should fail"; fi
echo "ok - exporting a missing image fails"

mkdir -p "$WORK/app" "$WORK/data" "$WORK/app2/mnt"
tar -xpf "$WORK/app.tar" -C "$WORK/app"
echo secret > "$WORK/data/file"

echo "# run"
assert_eq "user and groups are resolved in the container" \
  "$("$DROOT" run --root "$WORK/app" --user app -- id)" \
  "uid=1500(app) gid=1600(appgroup) groups=1600(appgroup),1700(extra)"
assert_eq "commands are looked up with the image's PATH" \
  "$("$DROOT" run --root "$WORK/app" --user app -- myapp)" "myapp-ok"
assert_eq "environment from .drootenv and --env" \
  "$("$DROOT" run --root "$WORK/app" -e JAVA_OPTS=-Xmx1g,-Xms1g -e NEW=1 -- sh -c 'echo "$JAVA_OPTS $NEW"')" \
  "-Xmx1g,-Xms1g 1"
assert_eq "capability bounding set" \
  "$("$DROOT" run --root "$WORK/app" -- awk '/^CapBnd/ {print $2}' /proc/self/status)" "00000000000004cb"
assert_eq "device nodes" \
  "$("$DROOT" run --root "$WORK/app" -- sh -c 'stat -c "%n %t:%T %a" /dev/null /dev/zero /dev/random /dev/urandom' | tr '\n' ' ')" \
  "/dev/null 1:3 666 /dev/zero 1:5 666 /dev/random 1:8 666 /dev/urandom 1:9 666 "
assert_eq "read-only bind mount" \
  "$("$DROOT" run --root "$WORK/app" --robind "$WORK/data:/mnt/data" -- sh -c 'cat /mnt/data/file; touch /mnt/data/x 2>/dev/null || echo ro')" \
  "$(printf 'secret\nro')"
set +e
"$DROOT" run --root "$WORK/app" -- sh -c 'exit 42'
code=$?
set -e
assert_eq "exit status is propagated" "$code" "42"

ln -sfn "$WORK/data/file" "$WORK/app/etc/hosts"
"$DROOT" run --root "$WORK/app" --cp -- true
assert_eq "--cp does not follow symlinks out of the root" "$(cat "$WORK/data/file")" "secret"

echo "# image without a shell"
mkdir -p "$WORK/scratch"
"$DROOT" export "$SCRATCH_IMAGE" | tar -xpf - -C "$WORK/scratch"
assert_eq "FROM scratch image with an unknown numeric uid" \
  "$("$DROOT" run --root "$WORK/scratch" --user 4242 --group 4343 -- /busybox sh -c 'echo "$GREETING"; /busybox id')" \
  "$(printf 'no shell here\nuid=4242 gid=4343 groups=4343')"

echo "# umount"
mount --bind "$WORK/data" "$WORK/app2/mnt"
[[ $(mounts_under "$WORK/app") -gt 0 ]] || fail "run should leave mounts behind"
"$DROOT" umount --root "$WORK/app"
"$DROOT" umount --root "$WORK/scratch"
assert_eq "umount removes every mount under the root" "$(mounts_under "$WORK/app")" "0"
assert_eq "umount keeps mounts of sibling directories" "$(mounts_under "$WORK/app2")" "1"
umount "$WORK/app2/mnt"
"$DROOT" umount --root "$WORK/app"
echo "ok - umount is idempotent"
if "$DROOT" umount --root / 2>/dev/null; then fail "umount --root / must be refused"; fi
echo "ok - umount --root / is refused"

echo "All integration tests passed (work directory: $WORK)"
