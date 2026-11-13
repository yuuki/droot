# Security considerations

Droot is a deployment tool, **not a sandbox**. It changes the root directory
of a process and limits the capabilities it can ever gain, but by default it
does not use namespaces, cgroups or LSMs (`--unshare` adds a mount namespace,
and `--seccomp default` a seccomp filter). Treat a process run by droot as a process
running on the host with a different root directory.

## What droot does and does not isolate

| Aspect | droot | Docker (default) |
|---|---|---|
| File system root | `chroot(2)` | `pivot_root(2)` in a mount namespace |
| Mounts | Made in the host's mount namespace and persist after exit; private with `--unshare` | Private mount namespace |
| Processes | Host PID namespace: `/proc` shows every host process | PID namespace |
| Network | Host network | Network namespace |
| Users | Host user IDs; a user namespace when run rootless | Host user IDs (or a user namespace) |
| Capabilities | Bounding set reduced to CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID, NET_BIND_SERVICE | A similar, larger default set |
| `/proc`, `/sys` | `/proc` shows the host's processes; sensitive files masked, `/proc/sys` and `/sys` read-only (like Docker) | Read-only or masked sensitive paths |
| Resource limits | None | cgroups |
| Syscall filtering, no_new_privs | `no_new_privs` by default; a seccomp filter like Docker's default profile with `--seccomp default` | seccomp profile, AppArmor/SELinux, `no-new-privileges` option |

## Recommendations

- **Always run applications as an unprivileged user** with `--user` (or a
  `USER` in the image). A process running as root inside droot keeps
  `CAP_DAC_OVERRIDE`, `CAP_CHOWN` and `CAP_FOWNER`, so it can modify every
  file under the root directory and in `--bind` mounts; drop them with
  `--cap-drop` if the application does not need them. `/proc` and `/sys` are
  views of the host's kernel: droot makes the files that change its settings
  read-only, but root can still read information about the whole host there,
  and `--no-mask` removes the protection.
- With images you did not build, give `--user` and `--group` as numeric IDs:
  a name is looked up in the image's `/etc/passwd`, which can map any name to
  uid 0 (droot prints a warning when it does).
- Only run **trusted images**. Without `no_new_privs` (on by default, off
  with `--no-new-privileges=false`), setuid-root programs in the image (`su`,
  `sudo`, `ping`, ...) let an unprivileged user regain the capabilities left in
  the bounding set.
- Use `--seccomp default` to reduce the kernel's attack surface: it denies
  creating namespaces (which an unprivileged user could otherwise do, and
  gain capabilities inside), the mount API, keyrings, `bpf`, `userfaultfd`,
  io_uring and other rarely needed system calls.
- Prefer `--robind` over `--bind` for data the application does not need to
  write.
- Do not give the application user write access to `ROOT_DIR` itself. droot
  resolves paths safely even if directories are swapped for symlinks, but the
  application could still, for instance, replace the binaries run next time.
- Wrap `droot run` with the sandboxing of your service manager when you need
  more isolation, e.g. systemd's `ProtectSystem=`, `ProtectKernelTunables=`,
  `PrivateTmp=`, `NoNewPrivileges=` (which also prevents the application from
  using setuid programs such as `sudo`), `MemoryMax=`, `CPUQuota=`.
- Extract archives with `tar -xpf` as root only if you trust their origin;
  verify a checksum or signature of the archive before deploying it.
- Keep the root directories in a directory that only root can access, e.g.
  `install -d -m 700 /var/containers`, and extract archives into one too
  (`mktemp -d`) before moving them into place. Extracted as root, a root
  directory holds the setuid-root programs of the image (`su`, `passwd`,
  `mount`, ...) with their owner and mode: where the users of the host can
  reach them, they can run them on the host, as root and outside droot, so
  that a vulnerable one in an old image gives them root. Its setgid
  programs give them a group such as `shadow` in the same way. The root
  directory itself must stay searchable (mode `0755`: `mkdir -m 755`,
  whatever the umask): the users of the container need it.
- Without `--unshare`, that directory does not protect the root directory
  from the users of the host with the UID of a process of the container:
  they can reach it through `/proc/PID/root` of the process while it runs,
  and run its setuid programs as root. With `--unshare`, the kernel ignores
  the setuid and setgid bits there, since the files are on mounts of another
  mount namespace. Use `--unshare`, or run the application under a UID that
  no user of the host has.

## Rootless mode

Run by a user other than root, droot runs in a user namespace (see the
[README](../README.md#running-without-root)). The kernel then enforces the
limits of the user who started droot: root in the container is that user,
the other users of the container are their subordinate IDs, and neither can
access the host's files beyond what the user could. Short of a kernel
vulnerability — user namespaces expose more of the kernel to unprivileged
users, the reason why some distributions restrict them — nothing droot or
`COMMAND` does grants more than the user has. Still:

- Inside, the same rules as above apply to the application and the root
  directory: a process running as root in the container can modify every
  file of the root directory and of the `--bind` mounts.
- The files you extract keep the setuid bits of the image: its setuid-root
  programs become setuid programs of your user. Keep the root directory
  inside a directory with mode 0700 of your own, or the other users of the
  host could run them as you.
- Without subordinate IDs, `setgroups(2)` is denied in the namespace, so the
  host's supplementary groups of the user stay with `COMMAND` (they show as
  `nobody`): their file permissions still apply.
- `/proc` is the host's (bind mounted): as with root, it shows every
  process of the host, with the same masked and read-only paths.
- `--seccomp default` also denies creating nested user namespaces.

## Hardening done by droot

- Mount targets, `--cp` destinations, device nodes and the files read from
  the container are resolved inside the root with file descriptors
  (`osutil.Root`): every component is opened with `openat(2)` and
  `O_NOFOLLOW` from the descriptor of its parent, symlinks are followed by
  droot as if the root were `/`, and the final operation uses the descriptor
  of the resolved directory (`mknodat`, `renameat`, mounting on
  `/proc/self/fd/N`, ...). Symlinks in an image cannot make droot mount on or
  write to host paths, even if a process of a running container swaps
  directories for symlinks while droot works.
- The mounts droot makes are slave mounts: nothing mounted inside the
  container propagates back to the host's directories.
- `droot umount` never follows symlinks and checks the mount ID of every
  mount point before detaching it.
- Device nodes are only created on the container's own file system and are
  owned by root; a bind mount of the host's `/dev` is left untouched.
- Mount targets are never the root directory itself and are never resolved
  through kernel pseudo file systems (procfs, sysfs, cgroupfs, tracefs, ...),
  where creating a directory changes the host's kernel state.
- Like Docker, sensitive files of `/proc` and `/sys` (`/proc/kcore`,
  `/proc/keys`, `/proc/timer_list`, `/sys/firmware`, and the debugfs,
  tracefs and securityfs mounted below the host's `/sys`, ...) are masked
  with a read-only bind mount of `/dev/null` or an empty read-only tmpfs,
  `/proc/sys`, `/proc/sysrq-trigger`, `/proc/irq`, `/proc/bus` and
  `/proc/fs` are read-only, and so is `/sys` with every mount below it; the
  bind mount of `/sys` is private, so mounts made on the host later (a cgroup
  hierarchy, fusectl, ...) do not appear in it (unless `--no-mask`). These mounts only hold because `CAP_SYS_ADMIN` is
  dropped: `--cap-add SYS_ADMIN` or `--no-dropcaps` let root undo them.
- `CAP_DAC_READ_SEARCH` is dropped: with `open_by_handle_at(2)` it allows
  reading any file of the host and escaping the chroot (the "shocker" exploit).
- `CAP_SYS_CHROOT`, `CAP_SYS_ADMIN`, `CAP_MKNOD`, `CAP_SYS_PTRACE`, ... are
  dropped from the bounding set, so not even setuid-root programs can regain
  them.
- The inheritable and ambient capability sets are emptied, so capabilities
  inherited from the invoking environment (pam_cap, systemd
  `CapabilityInheritable=`/`AmbientCapabilities=`) do not survive `execve(2)`.
- The capability bounding set and the credentials are changed on the thread
  that calls `execve(2)` (see [architecture](architecture.md#droot-run)).
- Supplementary groups are reset to the groups of `--user` in the container,
  so the command does not inherit the groups of the invoking root user.
- Under systemd, the host's notification socket (`NOTIFY_SOCKET`) is bound
  in the container read-only: its processes can send notifications, which
  systemd attributes to the unit of their cgroup and, with the default
  `NotifyAccess=main`, only accepts from its main process, but cannot
  `chmod` or `chown` the socket, which every service of the host uses.
  Rootless, the container gets a socket of the supervising droot instead,
  which relays the notifications of `COMMAND` only: systemd's socket is not
  reachable from the container, unless it is abstract (`@NAME`, reachable
  through the network namespace droot shares), no directory for the relay
  is writable (droot warns), or it is bound explicitly. The
  variables of socket activation are only passed on when `LISTEN_PID` is
  the PID of droot. `--no-systemd` passes none of these variables, binds
  no socket and relays nothing, e.g. when droot inherits a `NOTIFY_SOCKET` that is not meant
  for it (with `NotifyAccess=all`, the processes of the container could
  otherwise send that unit `STOPPING=1`, `FDSTORE=1`, ...). droot closes no descriptor it inherits: as with any
  program, those its parent leaves open (socket activation or not) reach
  `COMMAND`.
- `droot umount --root /` is refused.

## `droot unit`

`droot unit` writes the settings of a systemd service from the image: its
environment, its command, its user. systemd reads them as root, and a setting
added to the unit (e.g. `ExecStartPre=+...`, which runs on the host with full
privileges) would escape the container. Every value therefore goes through
[`internal/systemd/unitfile.go`](../internal/systemd/unitfile.go):

- Control characters are refused, so no value can end its line: a newline in
  an environment variable or an argument is an error, not an escape.
- Words are double-quoted, with `\` and `"` escaped, `%` (specifiers) doubled
  where systemd expands them (not in the options of `TemporaryFileSystem=`),
  and `$` (variables) doubled in `ExecStart=`. Paths cannot contain quotes,
  backslashes or trailing white space.
- The image never chooses a setting: users and groups are written as numeric
  IDs, `STOPSIGNAL` as a signal name from a fixed table (or left out), the
  names of environment variables only with letters, digits and underscores.
  The header quotes the image's name and ID.

The rules of each setting were checked with systemd 255. A fuzz test
checks that every value is refused or written as one line that these rules
read back as the value itself; it tests the quoting against a model of
systemd's parsing, not systemd itself. The unit tests also run
`systemd-analyze verify`, when it is installed, which reports the values
systemd refuses or ignores while parsing, but not those that fail later
(e.g. a mount option the kernel refuses).

The service gets the protections of `droot run` from systemd, with these
differences:

- systemd adds the host's supplementary groups of the user (`User=`), which
  may grant access to files of the host; droot sets the groups of the image.
  Check the groups of the host's user with these IDs.
- systemd's private `/dev` (`PrivateDevices=`) has the host's `/dev/pts`,
  `/dev/shm` and `/dev/hugepages`: a service running as root could open the
  terminals of the host's users and the shared memory of other services. The
  drop-in hides `/dev/pts` and `/dev/hugepages` (`InaccessiblePaths=`) and
  mounts a tmpfs of its own on `/dev/shm`, so the service has no
  pseudo-terminals. `PrivateDevices=` also removes `CAP_MKNOD` and
  `CAP_SYS_RAWIO`.
- The drop-in resets `User=`, `SupplementaryGroups=` and
  `CapabilityBoundingSet=`, so that your unit cannot widen them by accident
  (systemd ORs the lines of `CapabilityBoundingSet=`).
- The drop-in, and `systemctl show`, can be read by any user of the host:
  give secrets with an `EnvironmentFile=` of mode 0600, not with `--env`.
- An older systemd ignores the settings it does not know (with a warning in
  the journal): see the versions in the
  [deployment guide](deployment.md#7-run-with-systemd-alone).

## Reporting a vulnerability

Please report security issues privately, as described in
[SECURITY.md](../SECURITY.md), rather than in a public issue.
