#!/usr/bin/env bash
#
# End-to-end test of droot against a real Docker daemon.
#
# Requirements: Linux, root privileges, a running Docker daemon that can pull
# alpine and busybox. Usage:
#
#   make build && sudo ./script/integration-test.sh [path/to/droot]
#
# To test against Podman's Docker-compatible API, build the test images with
# podman and point droot to its socket:
#
#   sudo systemctl start podman.socket
#   sudo DOCKER=podman DOCKER_HOST=unix:///run/podman/podman.sock \
#     ./script/integration-test.sh ./droot
#
# On exit, the test images are removed, and so is the work directory unless
# KEEP_WORKDIR is set or mount points remain below it (deleting it would then
# delete files through the bind mounts).
set -euo pipefail

DROOT=$(realpath "${1:-./droot}")
REPO=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/droot-e2e.XXXXXX")
WORK=$(realpath "$WORK")
IMAGE=droot-e2e:latest
SCRATCH_IMAGE=droot-e2e-scratch:latest
LAYERS_IMAGE=droot-e2e-layers:latest
REGISTRY_CONTAINER=droot-e2e-registry
# The CLI used to build and remove the test images.
DOCKER=${DOCKER:-docker}

fail() { echo "FAIL: $*" >&2; exit 1; }
assert_eq() { # name actual expected
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
  echo "ok - $1"
}
mounts_under() { awk -v p="$1" '$5 == p || index($5, p "/") == 1' /proc/self/mountinfo | wc -l; }

cleanup() {
  local d pids
  # The helpers a failed test left running in the background.
  pids=$(jobs -p)
  # shellcheck disable=SC2086 # one PID per word
  if [[ -n $pids ]]; then kill $pids 2>/dev/null || true; fi
  for d in "$WORK"/*/; do
    "$DROOT" umount --root "$d" >/dev/null 2>&1 || true
  done
  $DOCKER rm -f "$REGISTRY_CONTAINER" >/dev/null 2>&1 || true
  $DOCKER rmi -f "$IMAGE" "$SCRATCH_IMAGE" "$LAYERS_IMAGE" ${REGISTRY_IMAGE:+"$REGISTRY_IMAGE"} >/dev/null 2>&1 || true
  if [[ -n ${SD_DIR:-} ]]; then rm -rf -- "$SD_DIR"; fi
  if [[ -n "${KEEP_WORKDIR:-}" ]]; then
    echo "work directory kept: $WORK"
  elif [[ $(mounts_under "$WORK") -eq 0 ]]; then
    rm -rf -- "${WORK:?}"
  else
    echo "mount points remain below $WORK: not deleting it" >&2
  fi
}
trap cleanup EXIT

[[ $(id -u) == 0 ]] || fail "must be run as root"
[[ -x "$DROOT" ]] || fail "droot binary not found at $DROOT (run 'make build')"

echo "# building test images"
$DOCKER build -q -t "$IMAGE" - >/dev/null <<'EOF'
FROM docker.io/library/alpine:3.22
RUN addgroup -g 1600 appgroup && adduser -D -u 1500 -G appgroup app \
 && addgroup -g 1700 extra && addgroup app extra \
 && mkdir -p /usr/local/myapp/bin \
 && printf '#!/bin/sh\necho myapp-ok "$@"\n' > /usr/local/myapp/bin/myapp \
 && chmod 755 /usr/local/myapp/bin/myapp
ENV PATH=/usr/local/myapp/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    JAVA_OPTS="-Dfoo=bar -Dx=y"
USER app
WORKDIR /usr/local/myapp
ENTRYPOINT ["myapp", "from-entrypoint"]
CMD ["from-cmd"]
EOF
$DOCKER build -q -t "$SCRATCH_IMAGE" - >/dev/null <<'EOF'
FROM docker.io/library/busybox:1.37-musl AS bb
FROM scratch
COPY --from=bb /bin/busybox /busybox
ENV GREETING="no shell here"
EOF

echo "# export"
"$DROOT" export -o "$WORK/app.tar" "$IMAGE"
assert_eq "export writes .drootenv from the image config" \
  "$(tar -xOf "$WORK/app.tar" .drootenv)" \
  "$(printf 'PATH=/usr/local/myapp/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nJAVA_OPTS=-Dfoo=bar -Dx=y')"
assert_eq "export writes the image config to .droot/config.json" \
  "$(tar -xOf "$WORK/app.tar" .droot/config.json | grep -cE '"(WorkingDir": "/usr/local/myapp|User": "app|from-entrypoint|from-cmd)"')" "4"
assert_eq "export leaves no temporary container" \
  "$($DOCKER ps -aq --filter label=io.github.yuuki.droot | wc -l)" "0"
(cd "$WORK" && "$DROOT" export --compress gzip --checksum -o app.tar.gz "$IMAGE" && sha256sum --quiet -c app.tar.gz.sha256) ||
  fail "--compress gzip --checksum: the checksum does not match"
assert_eq "--compress gzip" "$(tar -tzf "$WORK/app.tar.gz" | grep -c '^\.droot/config\.json$')" "1"
if command -v zstd >/dev/null; then
  "$DROOT" export --compress zstd "$IMAGE" | zstd -dc | tar -t >/dev/null || fail "--compress zstd"
  echo "ok - --compress zstd"
fi
"$DROOT" export --reproducible -o "$WORK/r1.tar" "$IMAGE"
sleep 1.1
"$DROOT" export --reproducible -o "$WORK/r2.tar" "$IMAGE"
cmp -s "$WORK/r1.tar" "$WORK/r2.tar" || fail "--reproducible: two exports of the same image differ"
echo "ok - --reproducible"
assert_eq "--exclude and --drop-docker-files" \
  "$("$DROOT" export --exclude /usr/share/apk --exclude '*.so.*' --drop-docker-files "$IMAGE" | tar -t | grep -cE '^(usr/share/apk|\.dockerenv$|.*\.so\.)' || true)" "0"
rm -f "$WORK/r1.tar" "$WORK/r2.tar" "$WORK/app.tar.gz" "$WORK/app.tar.gz.sha256"
PULL_IMAGE=docker.io/library/hello-world:latest
$DOCKER rmi -f "$PULL_IMAGE" >/dev/null 2>&1 || true
if "$DROOT" export --pull -o "$WORK/pulled.tar" "$PULL_IMAGE" 2>"$WORK/pull.err"; then
  assert_eq "--pull" "$(tar -tf "$WORK/pulled.tar" | grep -c '^hello$')" "1"
  rm -f "$WORK/pulled.tar"
elif grep -q "429 Too Many Requests" "$WORK/pull.err"; then
  echo "skip - --pull: Docker Hub rate limit"
else
  cat "$WORK/pull.err" >&2
  fail "--pull"
fi
$DOCKER rmi -f "$PULL_IMAGE" >/dev/null 2>&1 || true

echo "# export without daemon"
# Layers with whiteouts, replaced directories and a hard link to a deleted
# file: applied by droot, they must give the file system the daemon exports.
$DOCKER build -q -t "$LAYERS_IMAGE" - >/dev/null <<'EOF'
FROM docker.io/library/alpine:3.22
RUN mkdir -p /d/sub /opq/x && echo a > /d/a && echo b > /d/sub/b && echo keep > /opq/x/f \
 && ln /d/a /d/a-link && mkdir /todir && echo t > /todir/t && echo f > /file
RUN rm -rf /d/sub /d/a /opq /todir /file && mkdir -p /opq/new /file && echo n > /opq/new/n \
 && echo now-a-file > /todir && echo inside > /file/x
RUN rm -rf /usr/share/apk && echo changed >> /etc/motd
EOF
# listing ARCHIVE: the entries with their metadata, sorted, but those of
# droot and the files the daemon adds to (or changes in) containers.
listing() {
  tar --numeric-owner -tvf "$1" | awk '{ $4 = $4 " " $5; $5 = ""; print }' |
    grep -vE ' (\.droot/.*|\.droot/|\.drootenv|\.dockerenv|dev/.*|etc/|etc/(hostname|hosts|resolv\.conf|mtab)( -> .*)?)$' | sort
}
"$DROOT" export -o "$WORK/layers-daemon.tar" "$LAYERS_IMAGE"
listing "$WORK/layers-daemon.tar" >"$WORK/layers-daemon.list"
$DOCKER save -o "$WORK/layers-save.tar" "$LAYERS_IMAGE"
OCI_ARCHIVE="$WORK/layers-save.tar" # docker save writes an OCI layout too
if [[ $DOCKER == podman ]]; then
  OCI_ARCHIVE="$WORK/layers-oci.tar"
  podman save -q --format oci-archive -o "$OCI_ARCHIVE" "$LAYERS_IMAGE"
fi
for ref in "docker-archive:$WORK/layers-save.tar" "oci-archive:$OCI_ARCHIVE"; do
  DOCKER_HOST=tcp://127.0.0.1:1 "$DROOT" export -o "$WORK/layers-file.tar" "$ref"
  listing "$WORK/layers-file.tar" | diff "$WORK/layers-daemon.list" - || fail "export ${ref%%:*}: not the file system the daemon exports"
  echo "ok - export ${ref%%:*}: the file system the daemon exports"
done
assert_eq "export without daemon: the image configuration" \
  "$(tar -xOf "$WORK/layers-file.tar" .droot/config.json | grep -c '"Image": "oci-archive:')" "1"
# A registry on this machine, without TLS.
if $DOCKER run -d --name "$REGISTRY_CONTAINER" -p 127.0.0.1::5000 docker.io/library/registry:2 >/dev/null 2>"$WORK/registry.err"; then
  REGISTRY=$($DOCKER port "$REGISTRY_CONTAINER" 5000/tcp | head -n 1)
  REGISTRY_IMAGE=$REGISTRY/droot-e2e-layers:latest
  $DOCKER tag "$LAYERS_IMAGE" "$REGISTRY_IMAGE"
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [[ $DOCKER == podman ]]; then
      podman push -q --tls-verify=false "$REGISTRY_IMAGE" 2>/dev/null && break
    else
      docker push -q "$REGISTRY_IMAGE" >/dev/null 2>&1 && break
    fi
    sleep 1
  done
  DOCKER_HOST=tcp://127.0.0.1:1 "$DROOT" export -o "$WORK/layers-registry.tar" "docker://$REGISTRY_IMAGE"
  listing "$WORK/layers-registry.tar" | diff "$WORK/layers-daemon.list" - || fail "export docker://: not the file system the daemon exports"
  echo "ok - export docker://: the file system the daemon exports"
else
  echo "skip - export docker://: cannot start a registry: $(head -c 200 "$WORK/registry.err")"
fi
rm -f "$WORK"/layers-*

if [[ $DOCKER == docker ]] && docker system dial-stdio --help >/dev/null 2>&1; then
  # A fake ssh running `docker system dial-stdio` locally, against the
  # daemon of this test.
  mkdir -p "$WORK/fakessh"
  cat >"$WORK/fakessh/ssh" <<EOF
#!/bin/sh
while [ "\$1" != "--" ]; do shift; done
shift 2 # -- and the host
exec env DOCKER_HOST="${DOCKER_HOST:-unix:///var/run/docker.sock}" "\$@"
EOF
  chmod +x "$WORK/fakessh/ssh"
  assert_eq "ssh:// DOCKER_HOST" \
    "$(PATH="$WORK/fakessh:$PATH" DOCKER_HOST=ssh://user@remote:2222 "$DROOT" export "$IMAGE" | tar -t | grep -c '^\.droot/config\.json$')" "1"
fi
if "$DROOT" export does-not-exist:nope >/dev/null 2>&1; then fail "exporting a missing image should fail"; fi
echo "ok - exporting a missing image fails"

mkdir -p "$WORK/app" "$WORK/data" "$WORK/app2/mnt"
tar -xpf "$WORK/app.tar" -C "$WORK/app"
echo secret > "$WORK/data/file"

echo "# run"
assert_eq "without COMMAND, the image's ENTRYPOINT and CMD run as its USER" \
  "$("$DROOT" run --root "$WORK/app"; "$DROOT" run --root "$WORK/app" -- id -u)" \
  "$(printf 'myapp-ok from-entrypoint from-cmd\n1500')"
assert_eq "WORKDIR, HOME, USER and LOGNAME" \
  "$("$DROOT" run --root "$WORK/app" -- sh -c 'echo "$PWD $HOME $USER $LOGNAME"')" \
  "/usr/local/myapp /home/app app app"
cat >"$WORK/app.toml" <<EOF
# droot run --config
root = "$WORK/app"
user = "root"
env = ["FROM_CONFIG=1"]
command = ["sh", "-c", "echo \$FROM_CONFIG \$FROM_CLI \$(id -u)"]
EOF
assert_eq "--config, with the command line taking precedence" \
  "$("$DROOT" run --config "$WORK/app.toml"; "$DROOT" run --config "$WORK/app.toml" -e FROM_CLI=2 --user app)" \
  "$(printf '1 0\n1 2 1500')"
assert_eq "--user and --workdir override the image" \
  "$("$DROOT" run --root "$WORK/app" --user root -w /tmp -- sh -c 'echo "$(id -u) $PWD $HOME $USER"')" \
  "0 /tmp /root root"
assert_eq "user and groups are resolved in the container" \
  "$("$DROOT" run --root "$WORK/app" --user app -- id)" \
  "uid=1500(app) gid=1600(appgroup) groups=1600(appgroup),1700(extra)"
assert_eq "commands are looked up with the image's PATH" \
  "$("$DROOT" run --root "$WORK/app" --user app -- myapp)" "myapp-ok"
assert_eq "environment from .drootenv and --env" \
  "$("$DROOT" run --root "$WORK/app" -e JAVA_OPTS=-Xmx1g,-Xms1g -e NEW=1 -- sh -c 'echo "$JAVA_OPTS $NEW"')" \
  "-Xmx1g,-Xms1g 1"
printf 'FROM_FILE=1\nJAVA_OPTS=file\nPASSED\n' > "$WORK/app.env"
assert_eq "--env-file and --env KEY take values from droot's environment" \
  "$(PASSED=p FROM_HOST=h "$DROOT" run --root "$WORK/app" --env-file "$WORK/app.env" -e FROM_HOST -e UNSET_VAR -- \
      sh -c 'echo "$FROM_FILE $JAVA_OPTS $PASSED $FROM_HOST ${UNSET_VAR-unset}"')" \
  "1 file p h unset"
assert_eq "capability bounding set" \
  "$("$DROOT" run --root "$WORK/app" --user root -- awk '/^CapBnd/ {print $2}' /proc/self/status)" "00000000000004cb"
assert_eq "--ulimit" \
  "$("$DROOT" run --root "$WORK/app" --ulimit nofile=1024:2048 --ulimit core=0 -- sh -c 'ulimit -Sn; ulimit -Hn; ulimit -c')" \
  "$(printf '1024\n2048\n0')"
assert_eq "--cap-drop and --cap-add" \
  "$("$DROOT" run --root "$WORK/app" --user root --cap-drop ALL --cap-add net_raw --cap-add CAP_KILL -- awk '/^CapBnd/ {print $2}' /proc/self/status)" \
  "0000000000002020"
assert_eq "sensitive /proc files are masked, /proc/sys and /sys are read-only" \
  "$("$DROOT" run --root "$WORK/app" --user root -- sh -c 'wc -c < /proc/interrupts; echo x 2>/dev/null > /proc/sys/kernel/hostname || echo ro; touch /sys/kernel/x 2>/dev/null || echo ro')" \
  "$(printf '0\nro\nro')"
"$DROOT" umount --root "$WORK/app"
[[ $("$DROOT" run --root "$WORK/app" --user root --no-mask -- sh -c 'wc -c < /proc/interrupts') -gt 0 ]] ||
  fail "--no-mask: /proc/interrupts is masked"
echo "ok - --no-mask"
"$DROOT" umount --root "$WORK/app"
assert_eq "device nodes" \
  "$("$DROOT" run --root "$WORK/app" --user app -- sh -c 'stat -c "%n %t:%T %a %u" /dev/null /dev/zero /dev/full /dev/random /dev/urandom /dev/tty' | tr '\n' ' ')" \
  "/dev/null 1:3 666 0 /dev/zero 1:5 666 0 /dev/full 1:7 666 0 /dev/random 1:8 666 0 /dev/urandom 1:9 666 0 /dev/tty 5:0 666 0 "
assert_eq "/dev/pts, /dev/ptmx, /dev/shm and /dev/fd" \
  "$("$DROOT" run --root "$WORK/app" --user app -- sh -c 'readlink /dev/ptmx; stat -f -c %T /dev/pts /dev/shm; test -c /dev/pts/ptmx && echo ptmx; echo x > /dev/shm/f && cat /dev/shm/f; echo fd | cat /dev/stdin; ls /dev/fd/ >/dev/null && echo fds')" \
  "$(printf 'pts/ptmx\ndevpts\ntmpfs\nptmx\nx\nfd\nfds')"
assert_eq "--tmpfs" \
  "$("$DROOT" run --root "$WORK/app" --user root --tmpfs /tmp:size=1m --tmpfs /run:exec -- sh -c 'stat -f -c %T /tmp; printf "#!/bin/sh\necho exec-ok\n" > /tmp/x; chmod +x /tmp/x; /tmp/x 2>/dev/null || echo noexec; cp /tmp/x /run/x && /run/x')" \
  "$(printf 'tmpfs\nnoexec\nexec-ok')"
mkdir -p "$WORK/hostdev"
"$DROOT" run --root "$WORK/app" --bind "$WORK/hostdev:/dev" -- true 2>/dev/null
assert_eq "a bind mounted /dev is left alone, nothing is written into bind sources" "$(ls -A "$WORK/hostdev")" ""
"$DROOT" umount --root "$WORK/app"
assert_eq "read-only bind mount" \
  "$("$DROOT" run --root "$WORK/app" --user root --robind "$WORK/data:/mnt/data" -- sh -c 'cat /mnt/data/file; touch /mnt/data/x 2>/dev/null || echo ro')" \
  "$(printf 'secret\nro')"
assert_eq "--bind SRC:DEST:ro" \
  "$("$DROOT" run --root "$WORK/app" --user root --bind "$WORK/data:/mnt/data2:ro" -- sh -c 'cat /mnt/data2/file; touch /mnt/data2/x 2>/dev/null || echo ro')" \
  "$(printf 'secret\nro')"
assert_eq "--bind-net-files" \
  "$("$DROOT" run --root "$WORK/app" --user root --bind-net-files -- sh -c 'cat /etc/resolv.conf /etc/hosts; touch /etc/hosts 2>/dev/null || echo ro')" \
  "$(cat /etc/resolv.conf /etc/hosts; echo ro)"
"$DROOT" umount --root "$WORK/app"
assert_eq "no_new_privs is set by default, --no-new-privileges=false clears it" \
  "$("$DROOT" run --root "$WORK/app" --user app -- awk '/^NoNewPrivs/ {print $2}' /proc/self/status) $("$DROOT" run --root "$WORK/app" --user app --no-new-privileges=false -- awk '/^NoNewPrivs/ {print $2}' /proc/self/status)" \
  "1 0"
# With every capability, root can mount, unless the seccomp profile denies it.
assert_eq "--seccomp default" \
  "$("$DROOT" run --root "$WORK/app" --user root --no-dropcaps --no-mask -- sh -c 'mount -t tmpfs t /mnt && echo mounted && umount /mnt'; "$DROOT" run --root "$WORK/app" --user root --no-dropcaps --seccomp default -- sh -c 'mount -t tmpfs t /mnt 2>/dev/null || echo denied; awk "/^Seccomp:/ {print \$2}" /proc/self/status')" \
  "$(printf 'mounted\ndenied\n2')"
"$DROOT" umount --root "$WORK/app"
if command -v capsh >/dev/null; then
  assert_eq "inheritable capabilities of the caller are dropped" \
    "$(capsh --inh=cap_sys_admin -- -c "\"$DROOT\" run --root \"$WORK/app\" --user root -- awk '/^Cap(Inh|Eff)/ {print \$2}' /proc/self/status" | tr '\n' ' ')" \
    "0000000000000000 00000000000004cb "
fi
set +e
"$DROOT" run --root "$WORK/app" -- sh -c 'exit 42'
code=$?
set -e
assert_eq "exit status is propagated" "$code" "42"

ln -sfn "$WORK/data/file" "$WORK/app/etc/hosts"
"$DROOT" run --root "$WORK/app" --user root --cp -- true
assert_eq "--cp does not follow symlinks out of the root" "$(cat "$WORK/data/file")" "secret"

echo "# image without a shell"
mkdir -p "$WORK/scratch"
"$DROOT" export "$SCRATCH_IMAGE" | tar -xpf - -C "$WORK/scratch"
assert_eq "FROM scratch image with an unknown numeric uid" \
  "$("$DROOT" run --root "$WORK/scratch" --user 4242 --group 4343 -- /busybox sh -c 'echo "$GREETING $HOME ${USER-nouser}"; /busybox id')" \
  "$(printf 'no shell here / nouser\nuid=4242 gid=4343 groups=4343')"

echo "# mount lifecycle"
"$DROOT" umount --root "$WORK/app"
if "$DROOT" run --root "$WORK/app" --user root --bind "$WORK/data:/mnt/a" --bind "$WORK/nonexistent:/mnt/b" -- true 2>/dev/null; then
  fail "a run with a missing bind source should fail"
fi
assert_eq "a failed setup removes the mounts it made" "$(mounts_under "$WORK/app")" "0"
if "$DROOT" run --root "$WORK/app" --user root -- no-such-command 2>/dev/null; then
  fail "running a missing command should fail"
fi
assert_eq "a missing COMMAND removes the mounts made for it" "$(mounts_under "$WORK/app")" "0"
printf '#!/bin/sh\necho secret\n' > "$WORK/app/root-only" && chmod 700 "$WORK/app/root-only"
if "$DROOT" run --root "$WORK/app" --user 1500 -- /root-only 2>/dev/null; then
  fail "a COMMAND the user cannot execute should fail"
fi
assert_eq "a COMMAND the user cannot execute removes the mounts made for it" "$(mounts_under "$WORK/app")" "0"
rm -f "$WORK/app/root-only"
if "$DROOT" run --root "$WORK/app" --user "" -- id 2>/dev/null; then
  fail "an empty --user must not run COMMAND (as root)"
fi
echo "ok - an empty --user is refused"
assert_eq "--unshare: mounts in a private namespace" \
  "$("$DROOT" run --root "$WORK/app" --user root --unshare --tmpfs /tmp -- sh -c 'stat -f -c %T /proc /tmp'; mounts_under "$WORK/app")" \
  "$(printf 'proc\ntmpfs\n0')"
# Listening sockets of server-starter (dewy) or of systemd socket
# activation: droot replaces itself with COMMAND, which keeps them.
assert_eq "COMMAND inherits the descriptors of droot" \
  "$("$DROOT" run --root "$WORK/app" --user root --unshare -- sh -c 'cat <&3; cat <&5' 3<<<fd3 5<<<fd5)" \
  "$(printf 'fd3\nfd5')"
assert_eq "--hostname" \
  "$("$DROOT" run --root "$WORK/app" --user root --unshare --hostname droot-e2e -- sh -c 'echo "$(hostname) $HOSTNAME"'; [[ $(hostname) != droot-e2e ]] && echo host-unchanged)" \
  "$(printf 'droot-e2e droot-e2e\nhost-unchanged')"
assert_eq "--read-only" \
  "$("$DROOT" run --root "$WORK/app" --user root --read-only --tmpfs /tmp -- sh -c 'touch /x 2>/dev/null || echo ro; touch /tmp/x && echo tmp-ok'; mounts_under "$WORK/app"; touch "$WORK/app/x" && echo host-rw)" \
  "$(printf 'ro\ntmp-ok\n0\nhost-rw')"
rm -f "$WORK/app/x"
"$DROOT" run --root "$WORK/app" --user root -- sleep 30 &
sleeper=$!
for _ in $(seq 50); do "$DROOT" status --root "$WORK/app" | grep -q 'sleep 30' && break; sleep 0.1; done
status=$("$DROOT" status --root "$WORK/app")
kill "$sleeper"; wait "$sleeper" 2>/dev/null || true
grep -q "^Processes in $WORK/app: 1" <<<"$status" && grep -q "sleep 30" <<<"$status" && grep -qE '^ +/proc +proc' <<<"$status" ||
  fail "droot status: $status"
echo "ok - droot status"

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
# In a throwaway mount namespace: if the refusal regressed, this would detach
# every mount of the machine.
if unshare -m --propagation private true 2>/dev/null; then
  if unshare -m --propagation private "$DROOT" umount --root / 2>/dev/null; then
    fail "umount --root / must be refused"
  fi
  echo "ok - umount --root / is refused"
fi

echo "# dewy hook (examples/dewy/droot-extract-release)"
HOOK=$REPO/examples/dewy/droot-extract-release
# A file system with what the extraction of dewy loses: owners, a setuid
# file, a hard link, a symbolic link, a sticky directory.
src=$WORK/dewy/src
mkdir -p "$src/bin" "$src/tmp" "$src/home/app" "$WORK/dewy/releases/1"
printf 'x' >"$src/bin/tool"
chmod 4755 "$src/bin/tool"
ln "$src/bin/tool" "$src/bin/tool-link"
ln -s tool "$src/bin/tool-sym"
chmod 1777 "$src/tmp"
chmod 755 "$src/home/app"
chown 1500:1600 "$src/home/app"
rel=$WORK/dewy/releases/1
tar -czf "$rel/rootfs.tar.gz" --numeric-owner -C "$src" .
(cd "$rel" && sha256sum rootfs.tar.gz >rootfs.tar.gz.sha256)
ln -s "$rel" "$WORK/dewy/current"
(cd "$WORK/dewy" && "$HOOK" >/dev/null)
assert_eq "dewy hook: owners, modes, setuid bits and links are kept" \
  "$(ls "$rel"; cd "$rel/rootfs" && stat -c '%n %u:%g %a %h' bin/tool tmp home/app && readlink bin/tool-sym && [[ bin/tool -ef bin/tool-link ]] && echo hard-link)" \
  "$(printf 'rootfs\nrootfs.tar.gz.sha256\nbin/tool 0:0 4755 2\ntmp 0:0 1777 2\nhome/app 1500:1600 755 2\ntool\nhard-link')"
touch "$rel/rootfs/marker"
(cd "$WORK/dewy" && "$HOOK" >/dev/null)
[[ -e "$rel/rootfs/marker" ]] || fail "dewy hook: a release already extracted must be left alone"
echo "ok - dewy hook: a release already extracted is left alone"
# The archive of an image, run with droot.
rel=$WORK/dewy/releases/2
mkdir "$rel"
"$DROOT" export -q --compress gzip --checksum -o "$rel/rootfs.tar.gz" "$IMAGE"
ln -sfn "$rel" "$WORK/dewy/current"
(cd "$WORK/dewy" && "$HOOK" >/dev/null)
assert_eq "dewy hook: droot run --root current/rootfs" \
  "$("$DROOT" run --unshare --root "$WORK/dewy/current/rootfs")" "myapp-ok from-entrypoint from-cmd"
# A failure leaves no rootfs, and gives the space back.
rel=$WORK/dewy/releases/3
mkdir "$rel"
cp "$WORK/dewy/releases/2/rootfs.tar.gz.sha256" "$rel/"
printf 'not the archive' | gzip >"$rel/rootfs.tar.gz"
ln -sfn "$rel" "$WORK/dewy/current"
if (cd "$WORK/dewy" && "$HOOK" >/dev/null 2>&1); then
  fail "dewy hook: an archive whose checksum does not match must not be extracted"
fi
assert_eq "dewy hook: an archive whose checksum does not match is not extracted, and removed" \
  "$(ls "$rel")" "rootfs.tar.gz.sha256"

echo "# systemd"
# Notifications (sd_notify(3)) and socket activation (sd_listen_fds(3))
# without systemd: systemd-socket-activate passes the sockets, and sd.py
# plays both the notification socket of the service manager and, run by the
# host's python3 in a root directory made of the host's /usr, the service.
cat >"$WORK/sd.py" <<'EOF'
import os, socket, struct, sys

def address(a):  # NOTIFY_SOCKET: a path, or @NAME for an abstract socket
    return "\0" + a[1:] if a.startswith("@") else a

def pidfd_id():  # what LISTEN_PIDFDID must be (systemd 259 and later)
    try:
        fd = os.pidfd_open(os.getpid())
        try:
            return str(os.fstat(fd).st_ino)
        finally:
            os.close(fd)
    except (AttributeError, OSError):
        return None

if sys.argv[1] == "probe":
    # The service: prints the variables of systemd it got (PIDs that are
    # its own as "self"), accepts a connection on each of its sockets, and
    # sends READY=1 to NOTIFY_SOCKET, after trying to chmod it.
    pid = str(os.getpid())
    seen = []
    for k in ("NOTIFY_SOCKET", "LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES", "WATCHDOG_USEC", "WATCHDOG_PID"):
        v = os.environ.get(k)
        if v is not None:
            seen.append(k + "=" + ("self" if k.endswith("_PID") and v == pid else v))
    pidfdid = os.environ.get("LISTEN_PIDFDID")
    if pidfdid is not None and pidfdid != pidfd_id():
        seen.append("LISTEN_PIDFDID=not-self")
    print(" ".join(seen) or "none", flush=True)
    if os.environ.get("LISTEN_PID") == pid and pidfdid in (None, pidfd_id()):
        for fd in range(3, 3 + int(os.environ["LISTEN_FDS"])):
            conn, _ = socket.socket(fileno=fd).accept()
            conn.sendall(b"accepted on fd %d\n" % fd)
            conn.close()
    notify = os.environ.get("NOTIFY_SOCKET")
    if notify:
        if notify.startswith("/"):
            try:
                os.chmod(notify, 0o700)
                print("chmod: allowed", flush=True)
            except OSError as e:
                print("chmod: " + os.strerror(e.errno), flush=True)
        s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
        s.sendto(("READY=1\nSTATUS=pid " + pid).encode(), address(notify))
        print("notified", flush=True)
elif sys.argv[1] == "notify-server":
    # The service manager: receives a notification on ADDR, and tells
    # whether the process that sent it is the one it says it is.
    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_PASSCRED, 1)
    s.bind(address(sys.argv[2]))
    if sys.argv[2].startswith("/"):
        os.chmod(sys.argv[2], 0o777)
    print("listening", flush=True)
    s.settimeout(20)
    msg, ancillary, _, _ = s.recvmsg(4096, socket.CMSG_SPACE(12))
    sender = [struct.unpack("iii", d[:12])[0] for level, kind, d in ancillary
              if level == socket.SOL_SOCKET and kind == socket.SCM_CREDENTIALS][0]
    lines = msg.decode().split("\n")
    claimed = [int(l.split()[1]) for l in lines if l.startswith("STATUS=pid ")]
    print(" ".join(l for l in lines if not l.startswith("STATUS=")) + ", sent by " +
          ("the probe" if claimed == [sender] else "PID %d" % sender), flush=True)
elif sys.argv[1] == "connect":
    # A client of a socket of socket activation.
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(20)
    s.connect(sys.argv[2])
    print(s.makefile().readline().strip())
EOF
sd_skip=""
# The python3 to run in the container: the file itself, not a symlink that
# may lead out of /usr (e.g. to /etc/alternatives).
SD_PYTHON=$(realpath "$(command -v python3 || echo /nonexistent)" 2>/dev/null || true)
if ! command -v systemd-socket-activate >/dev/null; then
  sd_skip="systemd-socket-activate is not installed"
elif [[ $SD_PYTHON != /usr/* ]]; then
  sd_skip="python3 is not installed in /usr"
elif [[ ! -L /bin ]]; then
  sd_skip="/bin is not a symlink to usr/bin"
fi
# usr_root DIR: makes DIR a root directory for sd.py, whose programs are the
# host's /usr, bound with --robind /usr.
usr_root() {
  local l
  mkdir -p "$1/usr"
  for l in bin sbin lib lib32 lib64 libx32; do
    if [[ -L /$l ]]; then ln -sfn "$(readlink "/$l")" "$1/$l"; fi
  done
  cp "$WORK/sd.py" "$1/sd.py"
}
# wait_until COMMAND...: runs COMMAND until it succeeds, for 10 seconds at most.
wait_until() {
  local _
  for _ in $(seq 100); do
    if "$@"; then return 0; fi
    sleep 0.1
  done
  return 1
}
# notify_server ADDR OUT: starts a notification socket on ADDR in the
# background (its PID in notify_pid), which writes what it receives to OUT.
# assert_notified NAME OUT then waits for it (in this shell: a subshell
# cannot wait for it) and checks that it got READY=1 from the probe.
notify_server() {
  if [[ $1 == /* ]]; then rm -f "$1"; fi
  python3 "$WORK/sd.py" notify-server "$1" >"$2" &
  notify_pid=$!
  wait_until grep -q '^listening$' "$2" || fail "the notification socket $1 does not listen"
}
assert_notified() {
  wait "$notify_pid" || fail "$1: no notification ($(cat "$2"))"
  assert_eq "$1" "$(tail -n +2 "$2")" "READY=1, sent by the probe"
}
# activate PREFIX... -- ARGS...: runs systemd-socket-activate ARGS (droot
# run ..., after options of its own) with two sockets in $SD_SOCKETS (PREFIX
# runs systemd-socket-activate, e.g. as another user, who must be able to
# create them), connects to each, and prints what the probe printed and the
# replies.
activate() {
  local prefix=() activator
  while [[ $1 != -- ]]; do prefix+=("$1"); shift; done
  shift
  rm -f "$SD_SOCKETS/a.sock" "$SD_SOCKETS/b.sock"
  "${prefix[@]}" systemd-socket-activate -l "$SD_SOCKETS/a.sock" -l "$SD_SOCKETS/b.sock" --fdname=web:admin \
    "$@" >"$WORK/sd-activate.out" 2>"$WORK/sd-activate.err" &
  activator=$!
  wait_until test -S "$SD_SOCKETS/b.sock" || fail "systemd-socket-activate: $(cat "$WORK/sd-activate.err")"
  python3 "$WORK/sd.py" connect "$SD_SOCKETS/a.sock" >"$WORK/sd-replies" || true
  python3 "$WORK/sd.py" connect "$SD_SOCKETS/b.sock" >>"$WORK/sd-replies" || true
  wait "$activator" || fail "socket activation: $(cat "$WORK/sd-activate.err")"
  cat "$WORK/sd-activate.out" "$WORK/sd-replies"
}
# The probe, with the PID of droot as WATCHDOG_PID.
SD_PROBE=(sh -c 'export WATCHDOG_PID=$$; exec "$@"' sh)
if [[ -n $sd_skip ]]; then
  echo "skip - systemd: $sd_skip"
else
  # The sockets, in a directory with a short path: the path of a unix
  # socket has at most 108 bytes, and $WORK follows $TMPDIR.
  SD_DIR=$(mktemp -d /tmp/droot-sd.XXXXXX)
  chmod 755 "$SD_DIR"
  SD_SOCKETS=$SD_DIR
  sock=$SD_DIR/notify.sock
  usr_root "$WORK/sd"
  sd_run=(run --root "$WORK/sd" --unshare --robind /usr -- "$SD_PYTHON" /sd.py probe)
  assert_eq "socket activation: the sockets reach COMMAND, whose PID is LISTEN_PID" \
    "$(activate -- "$DROOT" "${sd_run[@]}")" \
    "$(printf 'LISTEN_PID=self LISTEN_FDS=2 LISTEN_FDNAMES=web:admin\naccepted on fd 3\naccepted on fd 4')"
  notify_server "$sock" "$WORK/sd-notify.out"
  assert_eq "notifications: NOTIFY_SOCKET and the watchdog reach COMMAND, the socket is bound read-only" \
    "$(NOTIFY_SOCKET="$sock" WATCHDOG_USEC=30000000 "${SD_PROBE[@]}" "$DROOT" "${sd_run[@]}")" \
    "$(printf 'NOTIFY_SOCKET=%s WATCHDOG_USEC=30000000 WATCHDOG_PID=self\nchmod: Read-only file system\nnotified' "$sock")"
  # WATCHDOG_PID=self: COMMAND has the PID of droot, the main process.
  assert_notified "notifications come from COMMAND" "$WORK/sd-notify.out"
  notify_server "@droot-e2e-$$" "$WORK/sd-abstract.out"
  NOTIFY_SOCKET="@droot-e2e-$$" "$DROOT" "${sd_run[@]}" >/dev/null || fail "droot run with an abstract NOTIFY_SOCKET"
  assert_notified "notifications to an abstract socket" "$WORK/sd-abstract.out"
  # Only the variables: the descriptors of droot are inherited anyway.
  assert_eq "the variables of the sockets and the watchdog of another process are not passed on" \
    "$(LISTEN_PID=1 LISTEN_FDS=1 WATCHDOG_USEC=1 WATCHDOG_PID=1 "$DROOT" "${sd_run[@]}")" "none"
  # droot may inherit a NOTIFY_SOCKET that is not meant for it.
  if ! NOTIFY_SOCKET="$SD_DIR/missing.sock" "$DROOT" run --root "$WORK/sd" --unshare --robind /usr -- true 2>"$WORK/sd.err"; then
    fail "a NOTIFY_SOCKET that cannot be bound must not be fatal: $(cat "$WORK/sd.err")"
  fi
  grep -q "COMMAND cannot send notifications" "$WORK/sd.err" || fail "no warning about the missing NOTIFY_SOCKET: $(cat "$WORK/sd.err")"
  echo "ok - a NOTIFY_SOCKET that cannot be bound is reported, not fatal"
  # The directories droot creates for the mount point do not follow the
  # umask (UMask=0077 of a unit): other users than root must reach it.
  usr_root "$WORK/sd-umask"
  notify_server "$sock" "$WORK/sd-notify.out"
  (umask 077 && NOTIFY_SOCKET="$sock" "$DROOT" run --root "$WORK/sd-umask" --unshare --robind /usr --user 65534 -- \
    "$SD_PYTHON" /sd.py probe >/dev/null) || fail "droot run with umask 077"
  assert_notified "notifications from another user than root, with umask 077" "$WORK/sd-notify.out"
  # --bind on the path of the socket wins.
  notify_server "$SD_DIR/proxy.sock" "$WORK/sd-proxy.out"
  NOTIFY_SOCKET="$sock" "$DROOT" run --root "$WORK/sd" --unshare --robind /usr --robind "$SD_DIR/proxy.sock:$sock" -- \
    "$SD_PYTHON" /sd.py probe >/dev/null || fail "droot run with a --bind on NOTIFY_SOCKET"
  assert_notified "a --bind on the path of NOTIFY_SOCKET is kept" "$WORK/sd-proxy.out"
  # In the host's mount namespace, the socket stays bound after the run,
  # and is bound afresh when the service manager creates it again.
  for i in 1 2; do
    notify_server "$sock" "$WORK/sd-notify.out"
    NOTIFY_SOCKET="$sock" "$DROOT" run --root "$WORK/sd" --robind /usr -- "$SD_PYTHON" /sd.py probe >/dev/null ||
      fail "droot run without --unshare, socket $i"
    assert_notified "notifications without --unshare, socket $i" "$WORK/sd-notify.out"
  done
  "$DROOT" umount --root "$WORK/sd"
  # A --bind of its directory makes the socket reachable already: droot must
  # not mount it over the host's own, which the host could then no longer
  # remove or replace.
  notify_server "$sock" "$WORK/sd-notify.out"
  NOTIFY_SOCKET="$sock" "$DROOT" run --root "$WORK/sd" --robind /usr --robind "$SD_DIR" -- "$SD_PYTHON" /sd.py probe >/dev/null ||
    fail "droot run with a --bind of the directory of NOTIFY_SOCKET"
  assert_notified "notifications through a --bind of the directory of the socket" "$WORK/sd-notify.out"
  rm "$sock" || fail "droot mounted on the host's socket: the host cannot remove it"
  echo "ok - the host's socket is not mounted on"
  "$DROOT" umount --root "$WORK/sd"
  # A --bind of another host directory over the directory of the socket:
  # droot creates no mount point in it (#56), with or without --unshare.
  mkdir "$WORK/sd-hostdir"
  notify_server "$sock" "$WORK/sd-notify.out"
  for unshare in --unshare ""; do
    # shellcheck disable=SC2086 # $unshare is one option or none
    NOTIFY_SOCKET="$sock" "$DROOT" run --root "$WORK/sd" $unshare --robind /usr --bind "$WORK/sd-hostdir:$SD_DIR" -- true 2>"$WORK/sd.err" ||
      fail "droot run ${unshare:-without --unshare} with a --bind over the directory of NOTIFY_SOCKET: $(cat "$WORK/sd.err")"
    grep -q "COMMAND cannot send notifications" "$WORK/sd.err" || fail "no warning about the socket that is not bound: $(cat "$WORK/sd.err")"
    assert_eq "${unshare:-no --unshare}: no mount point of NOTIFY_SOCKET in a host directory bound over its directory" \
      "$(ls -A "$WORK/sd-hostdir")" ""
  done
  "$DROOT" umount --root "$WORK/sd"
  # In a --tmpfs, it is created, also with --read-only.
  NOTIFY_SOCKET="$sock" "$DROOT" run --root "$WORK/sd" --read-only --robind /usr --tmpfs "$SD_DIR" -- "$SD_PYTHON" /sd.py probe >/dev/null ||
    fail "droot run with a --tmpfs on the directory of NOTIFY_SOCKET"
  assert_notified "notifications through a --tmpfs on the directory of the socket" "$WORK/sd-notify.out"
  # --no-systemd: no variable, no mount and no warning (#57).
  assert_eq "--no-systemd: none of the variables of systemd reach COMMAND" \
    "$(NOTIFY_SOCKET="$sock" WATCHDOG_USEC=30000000 "${SD_PROBE[@]}" "$DROOT" run --no-systemd "${sd_run[@]:1}")" "none"
  printf 'root = "%s"\nrobind = ["/usr"]\nno-systemd = true\ncommand = ["true"]\n' "$WORK/sd" >"$WORK/sd-no-systemd.toml"
  for notify in "$sock" "$SD_DIR/missing.sock"; do
    NOTIFY_SOCKET="$notify" "$DROOT" run --config "$WORK/sd-no-systemd.toml" 2>"$WORK/sd.err" ||
      fail "droot run with no-systemd = true: $(cat "$WORK/sd.err")"
    assert_eq "no-systemd = true, NOTIFY_SOCKET=${notify##*/}: nothing bound, no warning" \
      "$(mounts_under "$WORK/sd$notify") $(cat "$WORK/sd.err")" "0 "
  done
  "$DROOT" umount --root "$WORK/sd"
fi

echo "# rootless"
rootless_skip=""
if [[ $(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 1) == 0 ]]; then
  rootless_skip="user.max_user_namespaces is 0"
elif [[ $(cat /proc/sys/kernel/unprivileged_userns_clone 2>/dev/null || echo 1) == 0 ]]; then
  rootless_skip="kernel.unprivileged_userns_clone is 0"
elif [[ $(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null || echo 0) == 1 ]]; then
  rootless_skip="kernel.apparmor_restrict_unprivileged_userns is 1"
elif ! command -v setpriv >/dev/null; then
  rootless_skip="setpriv is not installed"
fi
if [[ -n $rootless_skip ]]; then
  echo "skip - rootless: $rootless_skip"
else
  chmod 755 "$WORK"
  chmod 644 "$WORK/app.tar"
  # A copy the other users can execute (the checkout may be in a home
  # directory they cannot enter).
  RL_DROOT="$WORK/droot"
  install -m 755 "$DROOT" "$RL_DROOT"
  # as_user USER COMMAND...: runs COMMAND as USER, with its own runtime
  # directory for the locks of droot. user_prefix USER sets AS_USER to the
  # command prefix doing it, for commands run in the background.
  user_prefix() {
    mkdir -p "$WORK/xdg-$1"
    chown "$1" "$WORK/xdg-$1"
    chmod 700 "$WORK/xdg-$1"
    AS_USER=(setpriv --reuid="$(id -u "$1")" --regid="$(id -g "$1")" --init-groups
      env -i PATH="$PATH" HOME=/ XDG_RUNTIME_DIR="$WORK/xdg-$1")
  }
  as_user() {
    user_prefix "$1"
    shift
    "${AS_USER[@]}" "$@"
  }
  if "$DROOT" unshare true 2>/dev/null; then fail "droot unshare must be refused to root"; fi
  echo "ok - droot unshare is refused to root"

  # A user without subordinate IDs: root is the only user.
  mkdir "$WORK/rl-nobody"
  chown nobody "$WORK/rl-nobody"
  as_user nobody tar -xf "$WORK/app.tar" -C "$WORK/rl-nobody"
  assert_eq "rootless without subordinate IDs: root only" \
    "$(as_user nobody "$RL_DROOT" run --root "$WORK/rl-nobody" --user 0 -- sh -c 'id -u; cat /proc/self/setgroups')" \
    "$(printf '0\ndeny')"
  if as_user nobody "$RL_DROOT" run --root "$WORK/rl-nobody" 2>"$WORK/rl.err"; then
    fail "rootless without subordinate IDs: the image's USER must be refused"
  fi
  grep -q "do not exist in the user namespace" "$WORK/rl.err" || fail "rootless: $(cat "$WORK/rl.err")"
  echo "ok - rootless without subordinate IDs: the image's USER is refused"
  # The pipe of the first stage must not take the place of an inherited
  # descriptor (the listening socket of server-starter is descriptor 3).
  assert_eq "rootless: COMMAND inherits the descriptors of droot" \
    "$(as_user nobody "$RL_DROOT" run --root "$WORK/rl-nobody" --user 0 -- sh -c 'cat <&3; cat <&5' 3<<<fd3 5<<<fd5)" \
    "$(printf 'fd3\nfd5')"
  # With socket activation, the sockets are 3 and 4: the descriptors after
  # them must not be taken by the pipe either.
  assert_eq "rootless: COMMAND inherits the descriptors after the sockets of socket activation" \
    "$(as_user nobody sh -c 'export LISTEN_PID=$$ LISTEN_FDS=2; exec "$0" run --root "$1" --user 0 -- sh -c "cat <&3; cat <&4; cat <&5"' "$RL_DROOT" "$WORK/rl-nobody" 3<<<fd3 4<<<fd4 5<<<fd5)" \
    "$(printf 'fd3\nfd4\nfd5')"

  if [[ -n $sd_skip ]]; then
    echo "skip - rootless systemd: $sd_skip"
  else
    mkdir "$WORK/rl-sd"
    usr_root "$WORK/rl-sd"
    chown -hR nobody "$WORK/rl-sd"
    user_prefix nobody
    SD_SOCKETS=$SD_DIR/nobody
    mkdir -m 700 "$SD_SOCKETS"
    chown nobody "$SD_SOCKETS"
    rl_sd_run=(run --root "$WORK/rl-sd" --robind /usr --user 0 -- "$SD_PYTHON" /sd.py probe)
    # LISTEN_PIDFDID, like LISTEN_PID, names the supervising droot: it must
    # not reach COMMAND, whose sd_listen_fds(3) would refuse the sockets.
    assert_eq "rootless socket activation: the sockets keep their descriptors, LISTEN_PID is COMMAND's PID" \
      "$(activate "${AS_USER[@]}" -- -E LISTEN_PIDFDID=1 "$RL_DROOT" "${rl_sd_run[@]}")" \
      "$(printf 'LISTEN_PID=self LISTEN_FDS=2 LISTEN_FDNAMES=web:admin\naccepted on fd 3\naccepted on fd 4')"
    notify_server "$sock" "$WORK/sd-notify.out"
    assert_eq "rootless notifications: NOTIFY_SOCKET is bound read-only, WATCHDOG_PID is COMMAND's PID" \
      "$("${AS_USER[@]}" NOTIFY_SOCKET="$sock" WATCHDOG_USEC=30000000 "${SD_PROBE[@]}" "$RL_DROOT" "${rl_sd_run[@]}")" \
      "$(printf 'NOTIFY_SOCKET=%s WATCHDOG_USEC=30000000 WATCHDOG_PID=self\nchmod: Read-only file system\nnotified' "$sock")"
    # From COMMAND, a child of the main process (the supervising droot):
    # units need NotifyAccess=all.
    assert_notified "rootless notifications come from COMMAND" "$WORK/sd-notify.out"
    # No mount point can be created (in a read-only tmpfs): reported, not
    # fatal.
    if ! "${AS_USER[@]}" NOTIFY_SOCKET="$sock" "$RL_DROOT" run --root "$WORK/rl-sd" --robind /usr --tmpfs /tmp:ro --user 0 -- \
      true 2>"$WORK/sd.err"; then
      fail "rootless: a NOTIFY_SOCKET that cannot be bound must not be fatal: $(cat "$WORK/sd.err")"
    fi
    grep -q "COMMAND cannot send notifications" "$WORK/sd.err" || fail "rootless: no warning about NOTIFY_SOCKET: $(cat "$WORK/sd.err")"
    echo "ok - rootless: a NOTIFY_SOCKET that cannot be bound is reported, not fatal"
  fi

  # A user with subordinate IDs: ROOTLESS_USER, or the user who ran sudo.
  SUB_USER=${ROOTLESS_USER:-${SUDO_USER:-}}
  if [[ -n $SUB_USER ]] && grep -q "^$SUB_USER:" /etc/subuid && grep -q "^$SUB_USER:" /etc/subgid &&
    command -v newuidmap >/dev/null && command -v newgidmap >/dev/null; then
    mkdir "$WORK/rl"
    chown "$SUB_USER" "$WORK/rl"
    as_user "$SUB_USER" "$RL_DROOT" unshare tar -xpf "$WORK/app.tar" -C "$WORK/rl"
    first_subuid=$(grep -m 1 "^$SUB_USER:" /etc/subuid | cut -d: -f2)
    assert_eq "droot unshare: archives keep their owners" \
      "$(stat -c %u "$WORK/rl/home/app")" "$((first_subuid + 1500 - 1))"
    assert_eq "rootless: the image's ENTRYPOINT and CMD run as its USER" \
      "$(as_user "$SUB_USER" "$RL_DROOT" run --root "$WORK/rl")" "myapp-ok from-entrypoint from-cmd"
    assert_eq "rootless: user, devices, kernel file systems, private mounts" \
      "$(as_user "$SUB_USER" "$RL_DROOT" run --root "$WORK/rl" -- sh -c '
          id -u; touch ~/x && echo home-writable
          echo x >/dev/null && head -c 1 /dev/zero | od -An -tx1 | tr -d " "
          stat -f -c %T /proc /dev/pts /dev/shm; touch /sys/x 2>/dev/null || echo sys-ro'
        mounts_under "$WORK/rl")" \
      "$(printf '1500\nhome-writable\n00\nproc\ndevpts\ntmpfs\nsys-ro\n0')"
    assert_eq "rootless: exit status" \
      "$(as_user "$SUB_USER" "$RL_DROOT" run --root "$WORK/rl" -- sh -c 'exit 7' || echo $?)" "7"
    user_prefix "$SUB_USER"
    "${AS_USER[@]}" "$RL_DROOT" run --root "$WORK/rl" -- sleep 30 &
    supervisor=$!
    sleep 1
    kill -TERM "$supervisor"
    status=0
    wait "$supervisor" || status=$?
    assert_eq "rootless: signals are passed on to COMMAND" "$status" "143"
    "${AS_USER[@]}" "$RL_DROOT" run --root "$WORK/rl" -- sleep 31 &
    supervisor=$!
    sleep 1
    kill -KILL "$supervisor"
    wait "$supervisor" 2>/dev/null || true
    for _ in $(seq 20); do
      ps -eo stat=,args= | awk '$1 !~ /Z/ && $2 == "sleep" && $3 == "31"' | grep -q . || break
      sleep 0.1
    done
    assert_eq "rootless: COMMAND dies with droot" \
      "$(ps -eo stat=,args= | awk '$1 !~ /Z/ && $2 == "sleep" && $3 == "31"' | wc -l)" "0"
    if [[ -z $sd_skip ]]; then
      # With the helpers newuidmap and newgidmap, which must not get the
      # sockets.
      mkdir "$WORK/rl-sd-sub"
      usr_root "$WORK/rl-sd-sub"
      chown -hR "$SUB_USER" "$WORK/rl-sd-sub"
      SD_SOCKETS=$SD_DIR/sub
      mkdir -m 700 "$SD_SOCKETS"
      chown "$SUB_USER" "$SD_SOCKETS"
      assert_eq "rootless socket activation with subordinate IDs" \
        "$(activate "${AS_USER[@]}" -- "$RL_DROOT" run --root "$WORK/rl-sd-sub" --robind /usr --user 0 -- "$SD_PYTHON" /sd.py probe)" \
        "$(printf 'LISTEN_PID=self LISTEN_FDS=2 LISTEN_FDNAMES=web:admin\naccepted on fd 3\naccepted on fd 4')"
    fi
  else
    echo "skip - rootless with subordinate IDs: set ROOTLESS_USER to a user with entries in /etc/subuid and /etc/subgid (and install newuidmap)"
  fi
fi

echo "All integration tests passed"
