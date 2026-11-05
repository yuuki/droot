# droot command line reference

<!-- Generated from the command definitions by `make docs`; do not edit. -->

droot — a super-easy container with chroot, without docker.

```
droot [global options] COMMAND [options] [ARG...]
```

droot exports the file system of a Docker image as a tar archive (export), runs a command inside an extracted archive with chroot(2) (run), cleans up the mounts made by run (umount) and shows them (status).

Global options, accepted before and after COMMAND:

| Option | Description |
|---|---|
| `-D, --debug` | enable debug output [$DROOT\_DEBUG] |
| `-q, --quiet` | print errors only: no warnings, informational messages nor progress [$DROOT\_QUIET] |
| `--log-format FORMAT` | format of the messages on STDERR: text or json (one log/slog JSON object per line) [$DROOT\_LOG\_FORMAT] |
| `-h, --help` | show help |
| `-v, --version` | print the version |

Commands:

- [`droot export`](#droot-export): Export the file system of a Docker image as a tar archive
- [`droot run`](#droot-run): Run a command in a container root directory with chroot(2)
- [`droot umount`](#droot-umount): Unmount everything mounted under a root directory by 'droot run'
- [`droot status`](#droot-status): Show the mounts and the processes of a root directory
- [`droot completion`](#droot-completion): Print a shell completion script (bash, zsh, fish)

## droot export

Export the file system of a Docker image as a tar archive.

```
droot export [options] IMAGE[:TAG]
```

Creates a temporary container from IMAGE (without starting it), streams its file system as a tar archive and removes the container. The image's configuration is saved as /.droot/config.json and its environment as /.drootenv in the archive, for 'droot run'.

The Docker daemon is selected like the docker CLI does, from DOCKER\_HOST, DOCKER\_TLS\_VERIFY, DOCKER\_CERT\_PATH, DOCKER\_CONTEXT or the current docker context.

| Option | Description |
|---|---|
| `-o, --output FILE` | write the archive to FILE instead of STDOUT (written atomically) |
| `--platform PLATFORM` | export the PLATFORM variant of a multi-platform image, e.g. linux/amd64 |
| `--pull` | pull IMAGE (for --platform) from its registry first, with the credentials of the docker CLI |
| `--compress ALGO` | compress the archive with gzip, or zstd (with the zstd command) |
| `--checksum` | also write the SHA-256 checksum of the archive to FILE.sha256 (sha256sum format; needs -o) |
| `--reproducible` | make the archive depend only on the image: modification times later than $SOURCE\_DATE\_EPOCH or the image's creation time are set to it |
| `--exclude PATTERN` | leave out the files matching PATTERN and everything below matching directories; a PATTERN with a / matches absolute paths (/usr/share/doc), one without a / base names (\*.pyc) (repeatable) |
| `--drop-docker-files` | leave out the files Docker adds to every container: /.dockerenv and the empty /etc/hostname, /etc/hosts, /etc/resolv.conf and /dev/console |
| `-h, --help` | show help |

## droot run

Run a command in a container root directory with chroot(2).

```
droot run [options] [--] [COMMAND [ARG...]]
```

Mounts /proc, /sys and the requested bind mounts under ROOT\_DIR, creates the basic device files, chroots into ROOT\_DIR, drops capabilities from the bounding set, switches to --user/--group and executes COMMAND with the environment saved by 'droot export' plus --env-file and --env.

Without COMMAND, the ENTRYPOINT and CMD of the image saved by 'droot export' (/.droot/config.json) are run; COMMAND replaces both. The image's USER and WORKDIR are the defaults of --user and --workdir.

The options must come before COMMAND: everything from COMMAND on is passed to it verbatim. Mounts are left in place after COMMAND exits; remove them with 'droot umount'. Must be run as root.

| Option | Description |
|---|---|
| `-r, --root ROOT_DIR` | root directory to chroot into (required) |
| `-u, --user USER` | user (name or ID) to run COMMAND as (default: the image's USER, or droot's own user); names are looked up in ROOT\_DIR/etc/passwd, then on the host |
| `-g, --group GROUP` | group (name or ID) to run COMMAND as (default: the primary group of --user) |
| `-w, --workdir DIR` | working directory of COMMAND inside the container (default: the image's WORKDIR, or /) |
| `-b, --bind SRC[:DEST][:OPTS]` | bind mount SRC from the host on DEST (default: SRC) in the container; OPTS is a comma-separated list of ro, rw and rbind (recursive) (repeatable) |
| `--robind SRC[:DEST][:OPTS]` | read-only bind mount (same as --bind SRC:DEST:ro) (repeatable) |
| `--tmpfs PATH[:OPTS]` | mount a tmpfs on PATH in the container; OPTS are mount flags (ro, exec, ...: nosuid,nodev,noexec by default) and tmpfs options (size=64m, mode=1777, ...) (repeatable) |
| `--copy-files, --cp` | copy the host's /etc/group, /etc/passwd, /etc/resolv.conf, /etc/hosts into the container |
| `--bind-net-files` | read-only bind mount the host's /etc/resolv.conf and /etc/hosts into the container, so that COMMAND sees later changes made to them in place (unlike --cp) |
| `--no-dropcaps` | do not drop capabilities (dangerous) |
| `--cap-add CAP` | also keep the capability CAP (e.g. NET\_RAW, SYS\_PTRACE, or ALL) in the bounding set (repeatable) |
| `--cap-drop CAP` | drop the capability CAP (e.g. CHOWN, or ALL) from the ones kept by default (repeatable) |
| `--ulimit NAME=SOFT[:HARD]` | set a resource limit of COMMAND, e.g. nofile=65536 or core=0:unlimited (names of ulimit, as in docker run) (repeatable) |
| `--unshare` | make the mounts in a private mount namespace: they disappear when COMMAND and its children exit, and need no 'droot umount' |
| `--unshare-ipc` | run COMMAND in a private IPC namespace (System V IPC, POSIX message queues) |
| `--hostname NAME` | run COMMAND in a private UTS namespace with the host name NAME |
| `--read-only` | make the root directory read-only for COMMAND (implies --unshare); use --tmpfs or --bind for the paths it writes |
| `--no-mask` | do not mask the sensitive files of /proc and /sys nor make /proc/sys, /sys and the like read-only (dangerous) |
| `--no-new-privileges` | set no\_new\_privs: setuid programs and file capabilities cannot grant privileges to COMMAND |
| `-e, --env KEY[=VALUE]` | set an environment variable, overriding /.drootenv and --env-file; a bare KEY takes its value from droot's own environment (repeatable) |
| `--env-file FILE` | read KEY=VALUE lines from the host's FILE, overriding /.drootenv; a bare KEY takes its value from droot's own environment (repeatable) |
| `-h, --help` | show help |

## droot umount

Unmount everything mounted under a root directory by 'droot run'.

```
droot umount [options]
```

Lazily unmounts (MNT\_DETACH) every mount point below ROOT\_DIR, deepest first, and fails if some cannot be unmounted. ROOT\_DIR itself is never unmounted. It is safe to run when nothing is mounted.

| Option | Description |
|---|---|
| `-r, --root ROOT_DIR` | root directory used with 'droot run' (required) |
| `-h, --help` | show help |

## droot status

Show the mounts and the processes of a root directory.

```
droot status [options]
```

Lists the mount points below ROOT\_DIR, i.e. the mounts made by 'droot run' that 'droot umount' would remove, and the processes whose root directory is ROOT\_DIR: the commands started by 'droot run' and their children.

Processes of a 'droot run --unshare' are listed, but their mounts are in a private mount namespace and are not. Run as root to see every process.

| Option | Description |
|---|---|
| `-r, --root ROOT_DIR` | root directory used with 'droot run' (required) |
| `-h, --help` | show help |

## droot completion

Print a shell completion script (bash, zsh, fish).

```
droot completion bash|zsh|fish
```

Prints a script that completes the commands and options of droot.

```
bash:  source <(droot completion bash)            # e.g. in ~/.bashrc
zsh:   source <(droot completion zsh)             # e.g. in ~/.zshrc
fish:  droot completion fish > ~/.config/fish/completions/droot.fish
```

| Option | Description |
|---|---|
| `-h, --help` | show help |
