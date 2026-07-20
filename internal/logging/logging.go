// SPDX-License-Identifier: AGPL-3.0-only

// Package logging constructs Golem's structured text and JSON loggers.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/GolemMC/Golem/internal/config"
)

func New(cfg config.Logging, out io.Writer) (*slog.Logger, error) {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[strings.ToLower(cfg.Level)]
	if !ok {
		return nil, fmt.Errorf("unsupported log level %q", cfg.Level)
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(out, opts)), nil
	}
	return slog.New(slog.NewTextHandler(out, opts)), nil
}
