// SPDX-License-Identifier: AGPL-3.0-only

// Package server owns Golem's process lifecycle, startup, and shutdown ordering.
// It coordinates subsystems but does not implement packet codecs or world formats.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/GolemMC/Golem/internal/auth"
	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/diagnostics"
	"github.com/GolemMC/Golem/internal/game"
	"github.com/GolemMC/Golem/internal/session"
	"github.com/GolemMC/Golem/internal/version"
	"github.com/GolemMC/Golem/internal/world"
)

type Server struct {
	cfg      config.Config
	log      *slog.Logger
	world    *world.World
	game     *game.Game
	sessions *session.Server
	auth     auth.Verifier
	started  time.Time
}

func New(cfg config.Config, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log, started: time.Now()}
}

func (a *Server) Run(parent context.Context, stdin io.Reader) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	w, err := world.Open(a.cfg.World, a.log)
	if err != nil {
		return fmt.Errorf("open world: %w", err)
	}
	a.world = w
	a.auth = auth.NewMojangVerifier(a.cfg.Auth.LoginTimeout.Duration)
	a.game = game.New(a.cfg.Server.ViewDistance, a.cfg.Network.ChunkWorkers, w, a.log)
	netServer, err := session.New(session.Config{Server: a.cfg.Server, Auth: a.cfg.Auth, Network: a.cfg.Network}, session.Spawn{X: w.Metadata.Spawn.X, Y: w.Metadata.Spawn.Y, Z: w.Metadata.Spawn.Z}, a.game, a.auth, a.log)
	if err != nil {
		_ = w.Close(context.Background())
		return err
	}
	a.sessions = netServer
	var diag *diagnostics.Server
	if a.cfg.Diagnostics.Enabled {
		diag = diagnostics.New(a.cfg.Diagnostics, a, a.log)
		if err := diag.Start(); err != nil {
			_ = w.Close(context.Background())
			return err
		}
		if diagnostics.ExposedWithoutToken(a.cfg.Diagnostics) {
			a.log.Warn("diagnostics is exposed on all interfaces without bearer authentication")
		}
	}
	a.log.Info("server starting", "server_version", version.ServerVersion, "minecraft_version", version.MinecraftVersion, "protocol", version.ProtocolVersion, "world", a.cfg.World.Path, "online_mode", true, "listen", fmt.Sprintf("%s:%d", a.cfg.Server.Address, a.cfg.Server.Port), "max_players", a.cfg.Server.MaxPlayers, "diagnostics", fmt.Sprintf("%s:%d", a.cfg.Diagnostics.Address, a.cfg.Diagnostics.Port), "backup", w.BackupPath)
	go a.game.Run(ctx)
	if stdin != nil {
		go runConsole(ctx, stdin, a.log, a, cancel)
	}
	autosaveDone := make(chan struct{})
	go func() {
		defer close(autosaveDone)
		ticker := time.NewTicker(a.cfg.World.AutosaveInterval.Duration)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				saveCtx, c := context.WithTimeout(ctx, 30*time.Second)
				if err := a.Save(saveCtx); err != nil {
					a.log.Error("autosave failed", "error", err)
				}
				c()
			}
		}
	}()
	netErr := make(chan error, 1)
	go func() { netErr <- a.sessions.Listen(ctx) }()
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-netErr:
		if !session.IsClosed(err) {
			runErr = err
		}
		cancel()
	}
	a.log.Info("graceful shutdown started")
	_ = a.sessions.Close()
	<-autosaveDone
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if diag != nil {
		if err := diag.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = fmt.Errorf("shutdown diagnostics: %w", err)
		}
	}
	if err := a.world.Close(shutdownCtx); err != nil {
		a.log.Error("shutdown persistence failed", "error", err)
		if runErr == nil {
			runErr = err
		}
	}
	if runErr == nil {
		a.log.Info("graceful shutdown complete")
	}
	return runErr
}

func (a *Server) Save(ctx context.Context) error {
	if a.world == nil {
		return errors.New("world is not open")
	}
	return a.world.Save(ctx)
}
func (a *Server) Online() int {
	if a.sessions == nil {
		return 0
	}
	return a.sessions.Online()
}
func (a *Server) Players() (int, int) { return a.Online(), a.cfg.Server.MaxPlayers }
func (a *Server) DiagnosticSnapshot() diagnostics.Snapshot {
	snap := diagnostics.Snapshot{Uptime: time.Since(a.started), Players: a.Online(), MaxPlayers: a.cfg.Server.MaxPlayers}
	if a.game != nil {
		gameSnapshot := a.game.Snapshot()
		snap.LoadedChunks = gameSnapshot.LoadedChunks
		snap.PendingChunkLoads = gameSnapshot.PendingChunkLoads
		snap.Tick = diagnostics.TickSnapshot{Last: gameSnapshot.Tick.Last, Average: gameSnapshot.Tick.Average, TPS: gameSnapshot.Tick.TPS, Overruns: gameSnapshot.Tick.Overruns, Ticks: gameSnapshot.Tick.Ticks}
	}
	if a.world != nil {
		snap.Save = a.world.SaveState()
		snap.WorldLockHeld = a.world.LockHeld()
	}
	if a.auth != nil {
		snap.AuthenticationHealthy = a.auth.Healthy()
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	snap.MemoryBytes = m.Alloc
	snap.Goroutines = runtime.NumGoroutine()
	return snap
}

func IsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
