// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/logging"
	"github.com/GolemMC/Golem/internal/server"
	"github.com/GolemMC/Golem/internal/version"
)

type optionalInt struct {
	value int
	set   bool
}

func (v *optionalInt) String() string { return strconv.Itoa(v.value) }
func (v *optionalInt) Set(text string) error {
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	v.value, v.set = parsed, true
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "generate-config" {
		generateConfig(os.Args[2:])
		return
	}

	flags := flag.NewFlagSet("golem", flag.ExitOnError)
	configPath := flags.String("config", config.DefaultPath, "path to golem.toml")
	listen := flags.String("listen", "", "override Minecraft listener as address:port")
	diagnosticsListen := flags.String("diagnostics-listen", "", "override diagnostics listener as address:port")
	logLevel := flags.String("log-level", "", "override log level: debug, info, warn, or error")
	memoryLimit := flags.String("memory-limit", "", "Go runtime soft memory target, for example 512MiB or 0")
	worldPath := flags.String("world", "", "override the world directory")
	showVersion := flags.Bool("version", false, "print version and exit")
	var maxProcs, maxPlayers, viewDistance optionalInt
	flags.Var(&maxProcs, "max-procs", "maximum Go CPUs; 0 uses automatic detection")
	flags.Var(&maxPlayers, "max-players", "advertised and enforced player limit")
	flags.Var(&viewDistance, "view-distance", "chunk subscription radius")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: golem [options]\n       golem generate-config [--config path]\n\n")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nPrecedence: CLI flags > GOLEM_* environment variables > TOML > defaults.")
		fmt.Fprintln(flags.Output(), "Common environment variables: GOLEM_SERVER_PORT, GOLEM_WORLD_PATH, GOLEM_RUNTIME_MAX_PROCS, GOLEM_RUNTIME_MEMORY_LIMIT, GOLEM_DIAGNOSTICS_TOKEN.")
	}
	_ = flags.Parse(os.Args[1:])
	if *showVersion {
		fmt.Printf("%s %s (Minecraft %s, protocol %d)\n", version.ServerName, version.ServerVersion, version.MinecraftVersion, version.ProtocolVersion)
		return
	}

	cfg, load, err := config.LoadOrCreate(*configPath, os.Environ())
	if load.Generated {
		fmt.Println("It looks like this is Golem's first launch.")
		fmt.Printf("A new configuration file was created at %s.\n", load.Path)
		fmt.Println("Review the generated settings before exposing this server publicly.")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(2)
	}
	base := filepath.Dir(load.Path)
	overrides := config.Overrides{Listen: *listen, DiagnosticsListen: *diagnosticsListen, LogLevel: *logLevel, MemoryLimit: *memoryLimit, World: *worldPath}
	if maxProcs.set {
		overrides.MaxProcs = &maxProcs.value
	}
	if maxPlayers.set {
		overrides.MaxPlayers = &maxPlayers.value
	}
	if viewDistance.set {
		overrides.ViewDistance = &viewDistance.value
	}
	overrideWarnings, err := overrides.Apply(&cfg, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "command-line configuration error:", err)
		os.Exit(2)
	}
	load.Warnings = append(load.Warnings, overrideWarnings...)
	if cfg.Runtime.MaxProcs > 0 {
		runtime.GOMAXPROCS(cfg.Runtime.MaxProcs)
	}
	if cfg.Runtime.MemoryLimit > 0 {
		debug.SetMemoryLimit(int64(cfg.Runtime.MemoryLimit))
	}

	log, err := logging.New(cfg.Logging, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logging error:", err)
		os.Exit(2)
	}
	slog.SetDefault(log)
	log.Info("configuration loaded", "path", load.Path)
	for _, warning := range unique(load.Warnings) {
		log.Warn(warning)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var stdin *os.File
	if server.IsTerminal(os.Stdin) {
		stdin = os.Stdin
	}
	if err := server.New(cfg, log).Run(ctx, stdin); err != nil {
		log.Error("server stopped with an error", "error", err)
		os.Exit(1)
	}
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func generateConfig(args []string) {
	flags := flag.NewFlagSet("golem generate-config", flag.ExitOnError)
	path := flags.String("config", config.DefaultPath, "path to create")
	_ = flags.Parse(args)
	created, err := config.Generate(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate configuration:", err)
		os.Exit(2)
	}
	fmt.Printf("Created %s with mode 0600. Review it before starting Golem.\n", created)
}
