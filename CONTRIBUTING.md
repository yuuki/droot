# Contributing to droot

Thank you for helping. droot runs as root on the content of container
images, so changes are reviewed with care; small, focused pull requests are
the easiest to review.

- **Bugs and ideas**: open an issue (the templates ask for what helps). For a
  large change, open an issue first to agree on the approach;
  [documentation/roadmap.md](documentation/roadmap.md) lists known issues and
  ideas.
- **Security vulnerabilities**: do not open an issue; see
  [SECURITY.md](SECURITY.md).

## Making a change

1. Fork the repository and create a branch from `master`.
2. Make the change, with tests. Read
   [documentation/development.md](documentation/development.md) for the
   tests, the tools and how releases are made, and [AGENTS.md](AGENTS.md)
   for the full list of commands, the compatibility contracts and the
   security invariants (for instance: paths inside a container root are
   resolved with `osutil.Root`).
3. Run `make check` before pushing. It runs the linters, the audit of the
   workflows and the unit tests with the race detector. Also run
   `make test-root` (as root) and `make integration-test` (root and Docker)
   if you changed `run`, `umount` or `export`.
4. Describe user-visible changes under "Unreleased" in
   [CHANGELOG.md](CHANGELOG.md), which becomes the release notes, and in the
   README or the documentation.
5. Open a pull request and fill in its template.

Commit subjects are short and scoped by package or area, e.g.
`osutil: split Root.lookup and OpenMountTarget` or `docs: ...`, with the
reason in the body. droot depends only on `golang.org/x/sys`: please
discuss a new dependency in the issue first.

If you used an AI assistant, review and test its changes yourself as you
would your own: you are the author of the pull request.
