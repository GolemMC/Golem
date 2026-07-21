
# Security Policy

## Project status

Golem is experimental and has not released a stable version.

Security fixes currently target the latest commit on the `main` branch.

Older commits, private development branches, and unofficial builds may not receive security fixes.

## Reporting a vulnerability

Do not report security vulnerabilities through public GitHub issues or public Discussions.

Once GitHub private vulnerability reporting is enabled, use the repository's private vulnerability reporting feature.

Until then, contact the maintainers through an available private channel associated with the GolemMC organization.

Do not publish proof-of-concept code or technical details before the maintainers have had a reasonable opportunity to investigate the issue.

## What may qualify as a security issue

Examples include:

- Authentication bypasses
- Online-mode verification failures
- Remote crashes
- Remote code execution
- Path traversal
- Unauthorized file access
- Unauthorized world modification
- World corruption caused by untrusted input
- Packet parsing vulnerabilities
- Memory exhaustion
- Goroutine exhaustion
- Decompression bombs
- Denial-of-service vulnerabilities
- Diagnostics authentication bypass
- Secret or token exposure
- Incorrect permission handling
- Unsafe world locking
- Backup corruption or disclosure
- Malicious NBT or region data causing unsafe behavior

Ordinary bugs, incomplete gameplay behavior, missing features, and performance limitations should usually be reported through the normal issue tracker.

## Information to include

A useful report should include:

- A clear description of the vulnerability
- The affected commit or version
- The affected subsystem
- Reproduction steps
- Expected behavior
- Actual behavior
- Security impact
- Relevant logs with secrets removed
- A minimal proof of concept when safe
- Suggested mitigation, if known

Explain whether the issue affects:

- Remote clients
- Authenticated players
- Server administrators
- World files
- Diagnostics endpoints
- Build or release infrastructure

## Sensitive information

Do not send or publish:

- Private Minecraft worlds
- Access tokens
- Diagnostics bearer tokens
- Player authentication data
- Session data
- Private keys
- Personal player data
- Credentials
- Unredacted configuration files

Use minimal synthetic fixtures whenever possible.

If a private world is essential to reproduce the problem, contact the maintainers first and wait for safe transfer instructions.

## World corruption reports

World corruption and persistence issues are security-sensitive when they can be triggered remotely or cause unsafe file access.

Include:

- Whether the world was copied before testing
- The affected region and chunk coordinates
- Whether the problem occurs during read, write, backup, or shutdown
- Whether the world remains readable by Minecraft Java Edition 1.21.1
- Whether recovery from backup is possible
- The smallest reproducible fixture available

Do not upload an entire private world to a public issue.

## Disclosure process

The maintainers will try to:

1. Confirm receipt of the report
2. Reproduce and assess the issue
3. Determine affected versions or commits
4. Develop and test a fix
5. Coordinate disclosure when appropriate
6. Publish a security advisory if needed

Because Golem is currently maintained by a small team, fixed response times cannot be guaranteed.

Please avoid public disclosure until the issue has been investigated and a fix or mitigation is available.

## Supported versions

| Version | Supported |
|---|---|
| Latest `main` commit | Yes |
| Experimental development branches | Best effort |
| Unofficial forks and builds | No |
| Unreleased historical commits | No guarantee |

## Security updates

Security-related changes may be released without waiting for normal roadmap milestones.

Users should update to the latest supported version or commit after a security fix is announced.