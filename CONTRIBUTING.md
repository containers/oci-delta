# Contributing to `oci-delta`

Thank you for considering contributing to `oci-delta`! This guide covers everything you need to know to contribute effectively to the project.

This project is licensed under the [Apache License, Version 2.0](LICENSE). By contributing, you agree that your contributions will be licensed under the same terms.

## Types of Contributions

- **Bug reports and feature requests:** Open an [issue](https://github.com/containers/oci-delta/issues/new).
- **Code and documentation changes:** Follow the workflow below to submit a pull request.

## Prior to Contributing

Please check the [existing issues](https://github.com/containers/oci-delta/issues) and comment on one describing what you plan to do if you plan to complete it. This avoids duplicate work.

If there is no issue open that describes the change you want to make, open one and state what you plan on doing and why. This also allows maintainers and other contributors to weigh in during the early stages.

## Prerequisites

In order to properly build, test, and lint the project, you need to have the following installed:

- `golang` >= 1.26
- `golangci-lint` >= 2.12
- `make`
- `python3`, `podman`, `jq` (for testing)
- `qemu-kvm`, `qemu-img`, `libvirt`, `virt-install`, `sshpass` (for VM-mode E2E testing)
- `go-md2man` (for regenerating the man page)

## Getting Started

1. Fork the repository on GitHub. If you plan on contributing to multiple issues, this only needs to be forked once.
2. Pull your fork down locally.

```bash
git clone https://github.com/YOUR_USERNAME/oci-delta.git
cd oci-delta
git remote add upstream https://github.com/containers/oci-delta.git
```

3. Ensure that `main` is up to date on GitHub and locally, then create a branch that is named after the issue you aim to solve.

```bash
git switch main
git pull
git switch -c branch-name-here
```

4. Get started making your changes!

## Development Workflow

1. Add and commit your changes:

```bash
git add file-name
git commit -s -m "message here"
```

The `-s` flag adds a `Signed-off-by` trailer to your commit, certifying that you wrote the change or otherwise have the right to submit it. All commits must be signed off this way; this is also checked automatically by a DCO status check once the PR is opened on GitHub.

When preparing the commit message, follow the [Git Commit Style](#git-commit-style) guidelines below.

If you forget to sign a commit, you can amend it:

```bash
git commit --amend -s
```

**Note**: All commits in your PR must be signed off. PRs with unsigned commits will not be merged.

2. Push your branch to your fork:

```
git push origin branch-name-here
```

## Commit Signature Verification

All commits must have **verified signatures** (shown as a "Verified" badge on GitHub). This is enforced by branch protection rules - commits without verified signatures will be rejected.

To set up commit signing, see GitHub's documentation on [commit signature verification](https://docs.github.com/en/authentication/managing-commit-signature-verification).

## Git Commit Style

This project follows the [Conventional Commits](https://www.conventionalcommits.org) specification.

### Commit Message Format

```
<type>: <description>

[optional body]

[optional footer(s)]

Signed-off-by: Your Name <your.email@example.com>
```

### Commit Types

- **feat**: A new feature
- **fix**: A bug fix
- **docs**: Documentation changes
- **test**: Adding or updating tests
- **ci**: Changes to CI/CD workflows
- **refactor**: Code refactoring without changing functionality
- **perf**: Performance improvements
- **chore**: Maintenance tasks, dependency updates

## Preparing a PR

All of these steps should be done and run successfully prior to submitting your PR.

### Code Quality

```bash
make fmt
make lint
```

This will autofix many issues. However, if any do appear after running these commands, then fix them based on the given description and rerun after you've saved your fix.

If your changes touch `go.mod`, run `go mod tidy` to keep `go.sum` in sync.

### Testing

If anything fails when running `make test` or `make test-coverage`, ensure those problems are fixed before creating a PR.

**Run all tests:**

```
make test
```

**When editing tests:**

```
make test-coverage
```

It would be good to run this before and after making your changes to catch any regressions. The coverage report is printed to stdout.

### Documentation

If you change any CLI flags, commands, or their behavior, update [README.md](README.md) and regenerate the man page:

```
make man
```

## Pull Request Process

**Ensure your PR**

- Clearly describes what it does and why.
- References a related issue.
- Passes all CI checks.
- Follows commit message conventions.
- Has changes sectioned into different commits if necessary.

**During code review**

- At least one maintainer or collaborator will review your PR before being merged.
  - Other individuals, such as other contributors, may also submit feedback even if they are not a maintainer or collaborator.
- Address any feedback or requested changes.
- Keep your branch up to date with `main` to minimize merge conflicts. This is especially the case when working with files that are pre-existing.
- **Approvals Required:** PRs require at least 1 approval from a maintainer or collaborator before they can be merged.

**After Approval**

- Once your PR has the required approval and all comments are resolved, a maintainer or collaborator will merge your PR.

## Security

If you discover a security vulnerability, please **do not** open a public issue. Refer to [SECURITY.md](./SECURITY.md) for responsible disclosure instructions.

## Questions or Issues?

If you have questions or run into issues:

- Look at our [existing issues](https://github.com/containers/oci-delta/issues).
- Open a [new issue](https://github.com/containers/oci-delta/issues/new) if needed.
- Feel free to ask questions in a PR, yours or otherwise.

If you notice anything missing from this guide or anything that is incorrect, please open an issue or a PR!

Thank you for contributing to `oci-delta` :)
