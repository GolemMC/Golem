// SPDX-License-Identifier: AGPL-3.0-only

// Package config owns defaults, strict TOML decoding, environment overrides,
// command-line overrides, validation, and secure first-launch generation.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const DefaultPath = "golem.toml"

type Config struct {
	Server      Server      `toml:"server"`
	World       World       `toml:"world"`
	Auth        Auth        `toml:"auth"`
	Network     Network     `toml:"network"`
	Runtime     Runtime     `toml:"runtime"`
	Diagnostics Diagnostics `toml:"diagnostics"`
	Logging     Logging     `toml:"logging"`
}

type Server struct {
	Address      string `toml:"address"`
	Port         int    `toml:"port"`
	MOTD         string `toml:"motd"`
	MaxPlayers   int    `toml:"max_players"`
	ViewDistance int    `toml:"view_distance"`
	Favicon      string `toml:"favicon"`
}

type World struct {
	Path             string   `toml:"path"`
	AutosaveInterval Duration `toml:"autosave_interval"`
	BackupDirectory  string   `toml:"backup_directory"`
	BackupRetention  int      `toml:"backup_retention"`
	RequireBackup    bool     `toml:"require_backup"`
}

type Auth struct {
	OnlineMode   bool     `toml:"online_mode"`
	LoginTimeout Duration `toml:"login_timeout"`
}

type Network struct {
	IdleTimeout             Duration `toml:"idle_timeout"`
	MaxPacketBytes          Bytes    `toml:"max_packet_size"`
	MaxUnauthenticated      int      `toml:"max_unauthenticated_connections"`
	StatusRequestsPerMinute int      `toml:"status_requests_per_minute"`
	MaxChatLength           int      `toml:"max_chat_length"`
	ChunkWorkers            int      `toml:"chunk_workers"`
}

type Runtime struct {
	MaxProcs    int   `toml:"max_procs"`
	MemoryLimit Bytes `toml:"memory_limit"`
}

type Diagnostics struct {
	Enabled     bool   `toml:"enabled"`
	Address     string `toml:"address"`
	Port        int    `toml:"port"`
	BearerToken string `toml:"bearer_token"`
}

type Logging struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = value
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Bytes int64

func (b *Bytes) UnmarshalText(text []byte) error {
	value, err := ParseBytes(string(text))
	if err != nil {
		return err
	}
	*b = Bytes(value)
	return nil
}

func (b Bytes) MarshalText() ([]byte, error) { return []byte(FormatBytes(int64(b))), nil }

func ParseBytes(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "0" {
		return 0, nil
	}
	units := []struct {
		suffix string
		scale  int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1_000}, {"B", 1}}
	for _, unit := range units {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed < 0 || parsed > (1<<63-1)/unit.scale {
			return 0, fmt.Errorf("invalid memory size %q", value)
		}
		return parsed * unit.scale, nil
	}
	return 0, fmt.Errorf("invalid memory size %q; use values such as 512MiB, 4GiB, or 0", value)
}

func FormatBytes(value int64) string {
	if value == 0 {
		return "0"
	}
	for _, unit := range []struct {
		suffix string
		scale  int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if value%unit.scale == 0 {
			return fmt.Sprintf("%d%s", value/unit.scale, unit.suffix)
		}
	}
	return fmt.Sprintf("%dB", value)
}

func Defaults() Config {
	return Config{
		Server:      Server{Address: "0.0.0.0", Port: 25565, MOTD: "Golem Experimental Server", MaxPlayers: 20, ViewDistance: 10, Favicon: "server-icon.png"},
		World:       World{Path: "./world", AutosaveInterval: Duration{60 * time.Second}, BackupDirectory: "./backups", BackupRetention: 5, RequireBackup: true},
		Auth:        Auth{OnlineMode: true, LoginTimeout: Duration{30 * time.Second}},
		Network:     Network{IdleTimeout: Duration{60 * time.Second}, MaxPacketBytes: Bytes(2 << 20), MaxUnauthenticated: 64, StatusRequestsPerMinute: 30, MaxChatLength: 256, ChunkWorkers: 4},
		Runtime:     Runtime{},
		Diagnostics: Diagnostics{Enabled: true, Address: "0.0.0.0", Port: 9090},
		Logging:     Logging{Level: "info", Format: "text"},
	}
}

type LoadResult struct {
	Path      string
	Generated bool
	Warnings  []string
}

func Load(path string) (Config, error) {
	cfg, _, err := LoadOrCreate(path, os.Environ())
	return cfg, err
}

func Generate(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve configuration path: %w", err)
	}
	token, err := secureToken()
	if err != nil {
		return "", fmt.Errorf("generate diagnostics token: %w", err)
	}
	if err := writeInitial(abs, token); err != nil {
		return "", err
	}
	return abs, nil
}

func LoadOrCreate(path string, environ []string) (Config, LoadResult, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Config{}, LoadResult{}, fmt.Errorf("resolve configuration path: %w", err)
	}
	result := LoadResult{Path: abs}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		token, tokenErr := secureToken()
		if tokenErr != nil {
			return Config{}, result, fmt.Errorf("generate diagnostics token: %w", tokenErr)
		}
		if err := writeInitial(abs, token); err != nil {
			return Config{}, result, fmt.Errorf("generate first-launch configuration: %w", err)
		}
		result.Generated = true
	} else if err != nil {
		return Config{}, result, fmt.Errorf("inspect configuration %q: %w", abs, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return Config{}, result, fmt.Errorf("stat configuration %q: %w", abs, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("configuration file %s has permissions %04o; use 0600 because it may contain a diagnostics token", abs, info.Mode().Perm()))
	}

	cfg := Defaults()
	data, err := os.ReadFile(abs)
	if err != nil {
		return Config{}, result, fmt.Errorf("read configuration %q: %w", abs, err)
	}
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, result, fmt.Errorf("decode configuration %q: %w", abs, err)
	}
	if err := applyEnvironment(&cfg, environ); err != nil {
		return Config{}, result, err
	}
	resolvePaths(&cfg, filepath.Dir(abs))
	warnings, err := cfg.Validate()
	result.Warnings = append(result.Warnings, warnings...)
	if err != nil {
		return Config{}, result, err
	}
	return cfg, result, nil
}

func secureToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func writeInitial(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data := fmt.Sprintf(`[server]
address = "0.0.0.0"
port = 25565
motd = "Golem Experimental Server"
max_players = 20
view_distance = 10
favicon = "server-icon.png"

[world]
path = "./world"
autosave_interval = "60s"
backup_directory = "./backups"
backup_retention = 5
require_backup = true

[auth]
online_mode = true
login_timeout = "30s"

[network]
idle_timeout = "60s"
max_packet_size = "2MiB"
max_unauthenticated_connections = 64
status_requests_per_minute = 30
max_chat_length = 256
chunk_workers = 4

[runtime]
max_procs = 0
memory_limit = "0"

[diagnostics]
enabled = true
address = "0.0.0.0"
port = 9090
bearer_token = %q

[logging]
level = "info"
format = "text"
`, token)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

type Overrides struct {
	Listen            string
	DiagnosticsListen string
	LogLevel          string
	Debug             bool
	MaxProcs          *int
	MemoryLimit       string
	World             string
	MaxPlayers        *int
	ViewDistance      *int
}

func (o Overrides) Apply(cfg *Config, base string) ([]string, error) {
	if o.Listen != "" {
		address, port, err := splitListen(o.Listen)
		if err != nil {
			return nil, fmt.Errorf("--listen: %w", err)
		}
		cfg.Server.Address, cfg.Server.Port = address, port
	}
	if o.DiagnosticsListen != "" {
		address, port, err := splitListen(o.DiagnosticsListen)
		if err != nil {
			return nil, fmt.Errorf("--diagnostics-listen: %w", err)
		}
		cfg.Diagnostics.Address, cfg.Diagnostics.Port = address, port
	}
	if o.Debug {
		cfg.Logging.Level = "debug"
	} else if o.LogLevel != "" {
		cfg.Logging.Level = o.LogLevel
	}
	if o.MaxProcs != nil {
		cfg.Runtime.MaxProcs = *o.MaxProcs
	}
	if o.MemoryLimit != "" {
		value, err := ParseBytes(o.MemoryLimit)
		if err != nil {
			return nil, fmt.Errorf("--memory-limit: %w", err)
		}
		cfg.Runtime.MemoryLimit = Bytes(value)
	}
	if o.World != "" {
		cfg.World.Path = resolve(base, o.World)
	}
	if o.MaxPlayers != nil {
		cfg.Server.MaxPlayers = *o.MaxPlayers
	}
	if o.ViewDistance != nil {
		cfg.Server.ViewDistance = *o.ViewDistance
	}
	return cfg.Validate()
}

func applyEnvironment(cfg *Config, environ []string) error {
	values := make(map[string]string)
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	setString(values, "GOLEM_SERVER_ADDRESS", &cfg.Server.Address)
	setString(values, "GOLEM_SERVER_MOTD", &cfg.Server.MOTD)
	setString(values, "GOLEM_SERVER_FAVICON", &cfg.Server.Favicon)
	setString(values, "GOLEM_WORLD_PATH", &cfg.World.Path)
	setString(values, "GOLEM_WORLD_BACKUP_DIRECTORY", &cfg.World.BackupDirectory)
	setString(values, "GOLEM_DIAGNOSTICS_ADDRESS", &cfg.Diagnostics.Address)
	setString(values, "GOLEM_DIAGNOSTICS_TOKEN", &cfg.Diagnostics.BearerToken)
	setString(values, "GOLEM_LOGGING_LEVEL", &cfg.Logging.Level)
	setString(values, "GOLEM_LOGGING_FORMAT", &cfg.Logging.Format)
	if value, ok := values["GOLEM_DEBUG"]; ok {
		debug, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("environment GOLEM_DEBUG: %w", err)
		}
		if debug {
			cfg.Logging.Level = "debug"
		}
	}
	for key, target := range map[string]*int{
		"GOLEM_SERVER_PORT":                             &cfg.Server.Port,
		"GOLEM_SERVER_MAX_PLAYERS":                      &cfg.Server.MaxPlayers,
		"GOLEM_SERVER_VIEW_DISTANCE":                    &cfg.Server.ViewDistance,
		"GOLEM_WORLD_BACKUP_RETENTION":                  &cfg.World.BackupRetention,
		"GOLEM_NETWORK_MAX_UNAUTHENTICATED_CONNECTIONS": &cfg.Network.MaxUnauthenticated,
		"GOLEM_NETWORK_STATUS_REQUESTS_PER_MINUTE":      &cfg.Network.StatusRequestsPerMinute,
		"GOLEM_NETWORK_MAX_CHAT_LENGTH":                 &cfg.Network.MaxChatLength,
		"GOLEM_NETWORK_CHUNK_WORKERS":                   &cfg.Network.ChunkWorkers,
		"GOLEM_RUNTIME_MAX_PROCS":                       &cfg.Runtime.MaxProcs,
		"GOLEM_DIAGNOSTICS_PORT":                        &cfg.Diagnostics.Port,
	} {
		if err := envInt(values, key, target); err != nil {
			return err
		}
	}
	for key, target := range map[string]*bool{
		"GOLEM_AUTH_ONLINE_MODE":     &cfg.Auth.OnlineMode,
		"GOLEM_WORLD_REQUIRE_BACKUP": &cfg.World.RequireBackup,
		"GOLEM_DIAGNOSTICS_ENABLED":  &cfg.Diagnostics.Enabled,
	} {
		if err := envBool(values, key, target); err != nil {
			return err
		}
	}
	for key, target := range map[string]*Duration{
		"GOLEM_AUTH_LOGIN_TIMEOUT":      &cfg.Auth.LoginTimeout,
		"GOLEM_NETWORK_IDLE_TIMEOUT":    &cfg.Network.IdleTimeout,
		"GOLEM_WORLD_AUTOSAVE_INTERVAL": &cfg.World.AutosaveInterval,
	} {
		if value, ok := values[key]; ok {
			if err := target.UnmarshalText([]byte(value)); err != nil {
				return fmt.Errorf("environment %s: %w", key, err)
			}
		}
	}
	for key, target := range map[string]*Bytes{
		"GOLEM_NETWORK_MAX_PACKET_SIZE": &cfg.Network.MaxPacketBytes,
		"GOLEM_RUNTIME_MEMORY_LIMIT":    &cfg.Runtime.MemoryLimit,
	} {
		if value, ok := values[key]; ok {
			if err := target.UnmarshalText([]byte(value)); err != nil {
				return fmt.Errorf("environment %s: %w", key, err)
			}
		}
	}
	return nil
}

func setString(values map[string]string, key string, target *string) {
	if value, ok := values[key]; ok {
		*target = value
	}
}

func envInt(values map[string]string, key string, target *int) error {
	value, ok := values[key]
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("environment %s: %w", key, err)
	}
	*target = parsed
	return nil
}

func envBool(values map[string]string, key string, target *bool) error {
	value, ok := values[key]
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("environment %s: %w", key, err)
	}
	*target = parsed
	return nil
}

func resolvePaths(cfg *Config, base string) {
	cfg.World.Path = resolve(base, cfg.World.Path)
	cfg.World.BackupDirectory = resolve(base, cfg.World.BackupDirectory)
	if cfg.Server.Favicon != "" {
		cfg.Server.Favicon = resolve(base, cfg.Server.Favicon)
	}
}

func resolve(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func splitListen(value string) (string, int, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return "", 0, fmt.Errorf("expected address:port: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port: %w", err)
	}
	return host, port, nil
}

func (c Config) Validate() ([]string, error) {
	var errs []error
	var warnings []string
	if !c.Auth.OnlineMode {
		errs = append(errs, errors.New("auth.online_mode must remain true; Golem does not support offline mode"))
	}
	if net.ParseIP(c.Server.Address) == nil && c.Server.Address != "localhost" {
		errs = append(errs, fmt.Errorf("server.address %q is not an IP address or localhost", c.Server.Address))
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, errors.New("server.port must be between 1 and 65535"))
	}
	if c.Server.MaxPlayers < 1 || c.Server.MaxPlayers > 1000 {
		errs = append(errs, errors.New("server.max_players must be between 1 and 1000"))
	} else if c.Server.MaxPlayers >= 20 {
		warnings = append(warnings, fmt.Sprintf("the configured %d-player limit is advertised but has not been performance validated", c.Server.MaxPlayers))
	}
	if c.Server.ViewDistance < 2 || c.Server.ViewDistance > 32 {
		errs = append(errs, errors.New("server.view_distance must be between 2 and 32"))
	} else if c.Server.ViewDistance > 16 {
		warnings = append(warnings, fmt.Sprintf("view distance %d is likely expensive for the experimental server", c.Server.ViewDistance))
	}
	if c.Server.MaxPlayers >= 20 && c.Server.ViewDistance >= 16 {
		warnings = append(warnings, "the configured player-limit and view-distance combination is likely unrealistic for the experimental server")
	}
	if c.Network.MaxPacketBytes < 1024 || c.Network.MaxPacketBytes > 8<<20 {
		errs = append(errs, errors.New("network.max_packet_size must be between 1KiB and 8MiB"))
	}
	if c.Auth.LoginTimeout.Duration <= 0 || c.Network.IdleTimeout.Duration <= 0 {
		errs = append(errs, errors.New("authentication and network timeouts must be positive"))
	}
	if c.Network.MaxUnauthenticated < 1 || c.Network.MaxUnauthenticated > 4096 {
		errs = append(errs, errors.New("network.max_unauthenticated_connections must be between 1 and 4096"))
	}
	if c.Network.StatusRequestsPerMinute < 1 || c.Network.StatusRequestsPerMinute > 10000 {
		errs = append(errs, errors.New("network.status_requests_per_minute must be between 1 and 10000"))
	}
	if c.Network.MaxChatLength < 1 || c.Network.MaxChatLength > 256 {
		errs = append(errs, errors.New("network.max_chat_length must be between 1 and 256"))
	}
	if c.Network.ChunkWorkers < 1 || c.Network.ChunkWorkers > 64 {
		errs = append(errs, errors.New("network.chunk_workers must be between 1 and 64"))
	}
	if c.World.Path == "" {
		errs = append(errs, errors.New("world.path is required"))
	} else if info, err := os.Stat(c.World.Path); err != nil {
		errs = append(errs, fmt.Errorf("world.path %q: %w", c.World.Path, err))
	} else if !info.IsDir() {
		errs = append(errs, fmt.Errorf("world.path %q is not a directory", c.World.Path))
	}
	if c.World.BackupDirectory == "" {
		errs = append(errs, errors.New("world.backup_directory is required"))
	} else if within(c.World.Path, c.World.BackupDirectory) {
		errs = append(errs, errors.New("world.backup_directory must be outside world.path"))
	}
	if c.World.BackupRetention < 1 {
		errs = append(errs, errors.New("world.backup_retention must be at least 1"))
	}
	if c.World.AutosaveInterval.Duration < time.Second {
		errs = append(errs, errors.New("world.autosave_interval must be at least 1s"))
	}
	if c.Runtime.MaxProcs < 0 {
		errs = append(errs, errors.New("runtime.max_procs cannot be negative"))
	}
	if c.Runtime.MemoryLimit < 0 {
		errs = append(errs, errors.New("runtime.memory_limit cannot be negative"))
	} else if c.Runtime.MemoryLimit > 0 && c.Runtime.MemoryLimit < 256<<20 {
		warnings = append(warnings, "the configured Go memory limit is very low and may cause excessive garbage collection")
	}
	if c.Diagnostics.Enabled {
		if c.Diagnostics.Port < 1 || c.Diagnostics.Port > 65535 {
			errs = append(errs, errors.New("diagnostics.port must be between 1 and 65535"))
		}
		if net.ParseIP(c.Diagnostics.Address) == nil && c.Diagnostics.Address != "localhost" {
			errs = append(errs, fmt.Errorf("diagnostics.address %q is not an IP address or localhost", c.Diagnostics.Address))
		}
		if c.Diagnostics.BearerToken == "" && !loopback(c.Diagnostics.Address) {
			warnings = append(warnings, "diagnostics is exposed beyond loopback without a bearer token")
		}
	}
	if c.Logging.Level != "debug" && c.Logging.Level != "info" && c.Logging.Level != "warn" && c.Logging.Level != "error" {
		errs = append(errs, errors.New("logging.level must be debug, info, warn, or error"))
	}
	if c.Logging.Format != "text" && c.Logging.Format != "json" {
		errs = append(errs, errors.New("logging.format must be text or json"))
	}
	return warnings, errors.Join(errs...)
}

func loopback(address string) bool {
	return address == "localhost" || address == "127.0.0.1" || address == "::1"
}

func within(parent, child string) bool {
	parentAbs, err1 := filepath.Abs(parent)
	childAbs, err2 := filepath.Abs(child)
	if err1 != nil || err2 != nil {
		return false
	}
	relative, err := filepath.Rel(parentAbs, childAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
