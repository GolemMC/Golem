# Contributing to Golem

Thank you for your interest in contributing to Golem.

Golem is an experimental Minecraft server written in Go. The project is still under active development, and internal APIs, package boundaries, configuration fields, and implementation details may change before version 1.0.0.

## Before contributing

Before starting work:

1. Check existing issues and pull requests.
2. Search GitHub Discussions for related ideas or architecture decisions.
3. Open an issue or Development discussion before beginning a large change.
4. Use a copied or disposable Minecraft world for all testing.

Small fixes and documentation improvements can usually be submitted directly through a pull request.

Draft pull requests are welcome when you want early feedback.

## Development requirements

You need:

- a Windows, macOS, or Linux machine
- Go 1.24 or newer
- Git
- Make
- A Minecraft 1.21.1 world for manual testing

Clone the repository:

```bash
git clone https://github.com/GolemMC/Golem.git
cd Golem
```

Install dependencies:

```bash
go mod download
```

Build Golem:

```bash
make build
```

## Running Golem

Start the server with:

```bash
./build/golem
```

Use a custom configuration file with:

```bash
./build/golem --config /path/to/golem.toml
```

Golem creates a default configuration file on first launch if one does not already exist.

Golem does not generate Minecraft worlds. You must provide an existing Minecraft 1.21.1 world.

## Branches

Create a new branch for each change.

Use descriptive branch names such as:

```text
feat/chunk-streaming
fix/session-timeout
refactor/world-storage
test/status-ping
docs/readme
```

Do not work directly on `main`.

## Pull requests

All changes should be submitted through pull requests.

Before opening a pull request:

* Rebase or update your branch from `main`
* Run formatting
* Run tests
* Run static analysis
* Remove debugging code
* Check that no worlds, backups, binaries, tokens, or private files are included

Pull requests should:

* Explain what changed
* Explain why the change is needed
* Include tests for behavior changes
* Describe compatibility impact
* Describe any world-safety impact
* Stay focused on one clear purpose

Large unrelated changes should be split into separate pull requests.

## Pull request titles

Pull request titles must follow Conventional Commits.

Examples:

```text
feat: add chunk subscription tracking
fix: prevent duplicate player sessions
refactor: separate protocol from gameplay
test: add status ping coverage
docs: explain world safety
chore: update dependencies
```

Optional scopes may be used:

```text
feat(protocol): add configuration acknowledgement
fix(world): reject overlapping region records
test(auth): add session verification coverage
```

## Commit messages

Clear commit messages are encouraged.

Individual commits do not need to be perfect because pull requests are squash merged, but commits should still describe meaningful changes where practical.

## Testing

Run the normal test suite:

```bash
make test
```

Run the race detector:

```bash
make test-race
```

Run static analysis:

```bash
make vet
```

Check formatting:

```bash
make fmt-check
```

Only submit changes after the relevant checks pass locally.

Behavior changes should include tests.

Bug fixes should include a regression test whenever practical.

## Coding standards

Follow normal Go conventions.

Contributions should:

* Use `gofmt`
* Prefer clear and explicit code
* Use descriptive package, type, variable, and function names
* Wrap errors with useful context
* Avoid global mutable state
* Avoid unbounded channels and goroutine creation
* Keep functions focused
* Keep package responsibilities clear
* Prefer typed commands over generic event maps
* Avoid unnecessary abstraction
* Avoid vague packages such as `utils`, `helpers`, `common`, or `manager`

## Architecture rules

Golem uses clear subsystem ownership.

General rules:

* Protocol code handles wire formats, not gameplay decisions
* Session code handles connection lifecycle, not world mutation
* Game code owns live authoritative gameplay state
* World code handles world storage and persistence
* Registry code owns Minecraft version-specific IDs and mappings
* Network goroutines must not directly mutate authoritative world or player state
* Disk work should remain bounded and coordinated
* Direct use of third-party Minecraft libraries should remain behind Golem-owned adapters

Changes that introduce new dependencies between major packages should be explained in the pull request.

## World safety

World corruption is treated as a critical failure.

Never test write behavior using an irreplaceable world.

Always use:

* Disposable worlds
* Copied worlds
* Synthetic test fixtures
* Backups

Never commit:

* Minecraft worlds
* Region files
* Player data
* Backups
* `session.lock`
* Diagnostics tokens
* Authentication data
* Private keys
* Built binaries
* Local configuration files containing secrets

Changes involving Anvil files, NBT, player data, chunk writes, or backups must include focused tests.

## Dependencies

Avoid adding dependencies unless they provide clear value.

When proposing a dependency:

* Explain why it is needed
* Check that it is actively maintained
* Check license compatibility
* Avoid large frameworks for small tasks
* Keep it behind a small internal adapter where practical

Golem is licensed under AGPL-3.0-only, so dependency licenses must be compatible.

## Reporting bugs

Use the bug report issue form.

Include:

* Golem commit or version
* Minecraft client version
* Operating system
* Reproduction steps
* Expected behavior
* Actual behavior
* Relevant logs with secrets removed

Do not upload private worlds, player data, credentials, or tokens.

Security vulnerabilities must not be reported through public issues.

## Proposing features

Use GitHub Discussions for broad ideas.

Open a feature request issue when the behavior is clearly defined and ready to be considered for implementation.

Feature proposals should explain:

* The problem being solved
* The proposed behavior
* Alternatives considered
* Compatibility impact
* Scope
* Testing expectations

## Community discussions

Use GitHub Discussions for:

* Questions
* Ideas
* Architecture discussions
* Implementation planning
* Contributor coordination

Keep discussions respectful, technical, and focused on the project.

## License

By contributing to Golem, you agree that your contribution may be distributed under the repository's AGPL-3.0-only license.
