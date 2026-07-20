// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strings"
)

type consoleActions interface {
	Save(context.Context) error
	Players() (online, maximum int)
}

func runConsole(ctx context.Context, reader io.Reader, log *slog.Logger, actions consoleActions, stop context.CancelFunc) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 1<<20))
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "":
		case "stop":
			log.Info("stop requested from console")
			stop()
			return
		case "save":
			if err := actions.Save(ctx); err != nil {
				log.Error("manual save failed", "error", err)
			} else {
				log.Info("manual save complete")
			}
		case "list":
			online, maximum := actions.Players()
			log.Info("players online", "current", online, "maximum", maximum)
		case "help":
			log.Info("console commands", "commands", "stop, save, list, help")
		default:
			log.Warn("unknown console command", "command", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		log.Debug("console input ended", "error", err)
	}
}
