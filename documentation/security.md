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
- `droot umount --root /` is refused.

## Reporting a vulnerability

Please report security issues privately, as described in
[SECURITY.md](../SECURITY.md), rather than in a public issue.
