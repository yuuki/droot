# Security policy

droot runs as root and executes the content of container images, so security
issues are taken seriously. See [Security considerations](documentation/security.md)
for what droot does and does not protect against: for instance, droot uses no
namespaces, so a command running as root in a droot container is close to
root on the host by design. Weaknesses of that model that are already
documented there are not vulnerabilities, but ideas to reduce them are
welcome as regular issues.

## Supported versions

Security fixes are made on the `master` branch and released in the next
version. Only the latest release is supported.

## Reporting a vulnerability

Please do **not** open a public issue, pull request or discussion.

Report the vulnerability privately with GitHub's
[private vulnerability reporting](https://github.com/yuuki/droot/security/advisories/new)
(the "Report a vulnerability" button of the Security tab). If that is not
possible, contact the author privately via the contact information on
https://github.com/yuuki.

Please include the droot version (`droot --version`), the command line, what
an attacker controls (the image, a process in the container, the host, the
Docker daemon, ...), and the impact. You should receive an answer within a
few weeks; droot is maintained on a best-effort basis.

## Verifying releases

Every binary and archive of a release has a signed build provenance
attestation, which shows that it was built from a release tag by the release
workflow of this repository, `checksums.txt` has a keyless cosign signature
by the same workflow, and the binaries are reproducible. See
[Verifying a release](README.md#verifying-a-release).
