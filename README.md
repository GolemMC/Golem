# Golem

An experimental Minecraft Java Edition server written in Go.

[![Validate](https://github.com/GolemMC/Golem/actions/workflows/validate.yml/badge.svg)](https://github.com/GolemMC/Golem/actions/workflows/validate.yml)
![Status](https://img.shields.io/badge/status-experimental-orange)
![Minecraft](https://img.shields.io/badge/Minecraft%20Java-1.21.1-62B47A)
![Go](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-AGPL--3.0--only-blue)

Golem is being built from scratch around a small set of clear package boundaries: sessions own connections, the game loop owns live state, and the world package owns disk data. The current server can accept a vanilla 1.21.1 client, authenticate it with Mojang, load an existing Overworld, and save a limited set of player and block changes.

Golem is not a replacement for a vanilla or Paper server yet. Use a copied or disposable world and keep the generated backup. Internal APIs and configuration may change before 1.0.

## Compatibility

- Minecraft Java Edition 1.21.1
- Protocol 767
- World data version 3955
- Linux
- Online mode only

Other client versions, offline mode, Bedrock Edition, and additional dimensions are not supported.

## Feature status

The labels below are intentionally conservative:

- **Working** means the path is implemented and covered by automated tests.
- **Partial** means it is usable in a narrow form but does not match normal vanilla behavior yet.
- **Planned** means there is no supported implementation today.

### Protocol and connections

- **Working** — server list status and favicon
- **Working** — online-mode RSA login, encrypted sessions, and Mojang profile verification
- **Working** — 1.21.1 login, configuration, registry, and play-state handshakes
- **Working** — bounded packet sizes, connection limits, timeouts, and outbound backpressure
- **Partial** — play packets for movement, chat, creative inventory, and basic block interaction
- **Planned** — additional Minecraft versions and protocol translation

### World

- **Working** — existing Anvil Overworld loading
- **Working** — world-version validation and exclusive world locking
- **Working** — startup backups with retention
- **Working** — serialized, atomic region and player-data writes
- **Working** — autosave, manual save, disconnect save, and synchronous shutdown save
- **Partial** — creative placement for simple blocks without state properties
- **Partial** — block breaking without drops, tools, or Survival rules
- **Partial** — entity-region files can be decoded, but entities are not spawned or ticked
- **Planned** — terrain generation, additional dimensions, lighting updates, fluids, and redstone

### Players and gameplay

- **Working** — joining, leaving, tab-list visibility, and basic multiplayer movement
- **Working** — multiplayer chat
- **Working** — position, rotation, selected hotbar, and inventory persistence
- **Partial** — creative inventory slot changes and bulk inventory clearing
- **Partial** — multiplayer block updates after successful disk persistence
- **Planned** — Survival mode, health, hunger, experience, combat, item drops, and crafting
- **Planned** — mobs, entity AI, vehicles, effects, bosses, and advancements

### Server operation

- **Working** — strict TOML configuration with environment and CLI overrides
- **Working** — secure first-launch configuration generation
- **Working** — compact text logs, JSON logs, and debug logging
- **Working** — health, readiness, Prometheus metrics, and JSON diagnostics endpoints
- **Working** — `help`, `list`, `save`, and `stop` console commands
- **Working** — graceful shutdown and Linux builds without CGO
- **Planned** — RCON, query, permissions, proxy support, and a public plugin API

## What should come next

The live backlog is tracked in the [Golem Roadmap](https://github.com/orgs/GolemMC/projects/1). The next useful milestones are:

1. Finish inventory and container rules, including item components, crafting, armor validation, and ordinary inventory clicks.
2. Add correct placement rules for directional, waterlogged, multipart, and block-entity-backed blocks.
3. Build the minimum Survival loop: health, hunger, damage, drops, tools, experience, and respawning.
4. Connect loaded entities to the game loop, then add ticking, movement, spawning, and saving.
5. Add terrain generation and dimension support without weakening the existing world-safety checks.
6. Expand commands and permissions before designing a stable plugin API.
7. Add protocol fuzzing, long-running multiplayer tests, and benchmarks before publishing an alpha release.

## Requirements

- Linux
- Go 1.24 or newer
- Git and Make
- An existing Minecraft Java Edition 1.21.1 world
- A Microsoft account that owns Minecraft Java Edition

Golem does not generate a world. Missing chunks are sent as empty chunks rather than being created on disk.

## Build

```bash
git clone https://github.com/GolemMC/Golem.git
cd Golem
make build
```

The Linux binary is written to `build/golem`.

## Run

```bash
./build/golem
```

Use another configuration file or override common settings from the command line:

```bash
./build/golem --config /path/to/golem.toml
./build/golem --world /path/to/world --listen 0.0.0.0:25565
```

Run `./build/golem --help` for the complete flag list.

## First launch

If the selected configuration file does not exist, Golem creates it with mode `0600` and generates a diagnostics bearer token. The token is stored in the configuration and is not printed to the terminal.

Startup stops if the configured world directory does not exist or is incompatible. When the world opens successfully, Golem takes a backup before accepting players unless backups are explicitly optional in the configuration.

## Logging

Text logs are compact by default. Use `--debug` or `GOLEM_DEBUG=true` for source locations and more runtime detail:

```bash
./build/golem --debug
```

Set `format = "json"` under `[logging]` when logs are collected by another service. Set `NO_COLOR` to disable terminal colors.

## Tests

Run the same checks used for pull requests:

```bash
make check
```

Useful individual targets are:

```bash
make test
make test-race
make vet
make fmt-check
make verify-generated
```

Tests that write world data use temporary fixtures. Manual testing should still use a copied or disposable world.

## Project layout

```text
cmd/golem/            Process entry point
internal/auth/        Online authentication
internal/config/      Configuration and first-launch setup
internal/diagnostics/ Health and metrics endpoints
internal/game/        Authoritative live gameplay state
internal/protocol/    Minecraft framing and wire types
internal/registry/    Versioned blocks, items, and registry data
internal/server/      Startup, console, autosave, and shutdown
internal/session/     Client connection and protocol state
internal/world/       Anvil, NBT, backups, and persistence
```

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Questions and design discussions belong in GitHub Discussions.

Do not report security problems in public issues. Follow [SECURITY.md](SECURITY.md) and do not upload private worlds, player data, credentials, or diagnostics tokens.

## License

Golem is licensed under [AGPL-3.0-only](LICENSE).
