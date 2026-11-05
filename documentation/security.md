# Security considerations

Droot is a deployment tool, **not a sandbox**. It changes the root directory
of a process and limits the capabilities it can ever gain, but it does not use
namespaces, cgroups, seccomp or LSMs. Treat a process run by droot as a process
running on the host with a different root directory.

## What droot does and does not isolate

| Aspect | droot | Docker (default) |
|---|---|---|
| File system root | `chroot(2)` | `pivot_root(2)` in a mount namespace |
| Mounts | Made in the host's mount namespace; persist after exit | Private mount namespace |
| Processes | Host PID namespace: `/proc` shows every host process | PID namespace |
| Network | Host network | Network namespace |
| Users | Host user IDs | Host user IDs (or a user namespace) |
| Capabilities | Bounding set reduced to CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID, NET_BIND_SERVICE | A similar, larger default set |
| `/proc`, `/sys` | Host's, writable by root | Read-only or masked sensitive paths |
| Resource limits | None | cgroups |
| Syscall filtering, no_new_privs | None | seccomp profile, AppArmor/SELinux |

## Recommendations

- **Always run applications as an unprivileged user** with `--user`. A process
  running as root inside droot keeps `CAP_DAC_OVERRIDE`, `CAP_CHOWN` and
  `CAP_FOWNER`, so it can modify every file under the root directory and in
  `--bind` mounts. Because `/proc` and `/sys` are the host's, root can also
  change host kernel settings through files such as `/proc/sys/*`,
  `/proc/sysrq-trigger` or sysfs attributes that are only protected by file
  permissions.
- Only run **trusted images**. setuid-root programs in the image (`su`,
  `sudo`, `ping`, ...) let an unprivileged user regain the capabilities left in
  the bounding set. Remove them from production images when possible.
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
- Device nodes are only created on the container's own file system and are
  owned by root; a bind mount of the host's `/dev` is left untouched.
- Mount targets are never the root directory itself and are never resolved
  through kernel pseudo file systems (procfs, sysfs, cgroupfs, tracefs, ...),
  where creating a directory changes the host's kernel state.
- `CAP_DAC_READ_SEARCH` is dropped: with `open_by_handle_at(2)` it allows
  reading any file of the host and escaping the chroot (the "shocker" exploit).
- `CAP_SYS_CHROOT`, `CAP_SYS_ADMIN`, `CAP_MKNOD`, `CAP_SYS_PTRACE`, ... are
  dropped from the bounding set, so not even setuid-root programs can regain
  them.
- The capability bounding set and the credentials are changed on the thread
  that calls `execve(2)` (see [architecture](architecture.md#droot-run)).
- Supplementary groups are reset to the groups of `--user` in the container,
  so the command does not inherit the groups of the invoking root user.
- `droot umount --root /` is refused.

## Reporting a vulnerability

Please report security issues privately to the author via the contact
information on https://github.com/yuuki rather than in a public issue.
