# Golem
A Minecraft server software made in Go.

[add badges]

## About
Golem is an experimental Miencraft server for Java Edition written in Go.

The project is focused on building a clean, readable and reliable server architecture from the grounds up. It currently only targets Minecraft's version 1.21.1 and supports vanilla clients.

Golem is currently not a suitable replacement for the vanilla server software provided by Minecraft. The current focus is protocol compatibility, world handling, muliplayer fundamentals and creating a strong foundation for future features like plugins and multi-version support.

## Project status

Golem is currently under active development.

the server software currently is incomplete and alot of features are unstable. The internal API and the package structure can and will change before 1.0.

Do NOT use Golem with important worlds. Always test with copies or disposable Minecraft worlds and keep backups.

## Supported versions

Golem currently supports:
- MC version 1.21.1
  - Protocol Version 767
- World data version 3955
- Linux
- Online-mode only

## Features
Current features:
- Minecraft server-list status response
- Online-mode authentication
- Vanilla Minecraft 1.21.1 client connections
- Existing Overworld chunk loading
- Basic player movement
- Multiplayer chat
- Server diagnostics
- Terminal commands
- Graceful shutdown
- World locking and version validation

## Current limitations
Golem is not yet a complete Minecraft server implementation.

Current limitations include:
- Minecraft Java Edition 1.21.1 only
- Overworld only
- No terrain generation
- No Survival mode
- No plugin system
- No multi-version protocol support
- No proxy or multi-server support
- Incomplete gameplay behavior
- No production stability guarantee
- No performance guarantee

## Requirements

- Linux
- Go 1.26 or newer
- A Minecraft Java Edition 1.21.1 world
- A premium minecraft account

## Building
Clone the repo:
```bash
git clone https://github.com/GolemMC/Golem.git
# or
git clone git@github.com:GolemMC/Golem.git
cd Golem
```
Build Golem with the provided Makefile:
```bash
make build
```
The compiled binary will be created at:
```
build/golem
```
You can also build it directly with Go:
```bash
go build -o build/golem ./cmd/golem
```

## Running
Run/Start Golem with:
```bash
./build/golem
```
use a custom config file with:
```bash
./build/golem --config /path/to/golem.toml
```

## First Launch
When Golem starts without an existing config file, it creates `golem.toml` automatically.

The generated config includes a secure diagnostics token and uses owner-friendly file permissions on linux

Golem then loads the generated config and continues startup.

If the configured world folder doesnt exist, startup stop with a clear error.

Golem does at the current state not create or generate a Minecraft world. You must provide an existing world yourself.

## Testing

Run the test suite:
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

## Project structure

```text
cmd/golem/            Golem executable
internal/auth/        Online authentication
internal/config/      Configuration loading and validation
internal/diagnostics/ Health and metrics endpoints
internal/game/        Authoritative gameplay state
internal/protocol/    Minecraft protocol encoding and decoding
internal/registry/    Minecraft version and registry data
internal/server/      Application lifecycle
internal/session/     Client connection lifecycle
internal/world/       World loading and persistence
```

## Contributing

Contributions are welcome.

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

Questions, ideas, and architecture discussions belong in GitHub Discussions.

## Security

Do not report security vulnerabilities through public issues.

Read [SECURITY.md](SECURITY.md) for private reporting instructions.

## License

Golem is licensed under the GNU Affero General Public License v3.0 only.

See [LICENSE](LICENSE) for the full license text.
