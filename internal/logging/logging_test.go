// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GolemMC/Golem/internal/config"
)

func TestConsoleHandlerProducesReadableStructuredOutput(t *testing.T) {
	var output bytes.Buffer
	handler := newConsoleHandler(&output, slog.LevelInfo, false)
	handler = handler.WithAttrs([]slog.Attr{slog.String("component", "session")}).(*consoleHandler)
	handler = handler.WithGroup("request").(*consoleHandler)

	record := slog.NewRecord(time.Date(2026, 7, 22, 18, 42, 10, 0, time.Local), slog.LevelInfo, "player joined", 0)
	record.AddAttrs(
		slog.String("username", "Alex"),
		slog.String("message", "hello world"),
		slog.Int("id", 42),
	)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	want := "[2026-07-22 18:42:10] INFO  player joined component=session request.username=Alex request.message=\"hello world\" request.id=42\n"
	if got := output.String(); got != want {
		t.Fatalf("console log:\n got: %q\nwant: %q", got, want)
	}
}

func TestDebugModeIncludesDebugEventsAndSource(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(config.Logging{Level: "debug", Format: "text"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("packet received", "packet_id", 12)

	got := output.String()
	for _, part := range []string{" DEBUG packet received", "source=internal/logging/logging_test.go:", "packet_id=12"} {
		if !strings.Contains(got, part) {
			t.Fatalf("debug log %q does not contain %q", got, part)
		}
	}
}

func TestInfoModeFiltersDebugEvents(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(config.Logging{Level: "info", Format: "text"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden detail")
	logger.Info("visible event")

	got := output.String()
	if strings.Contains(got, "hidden detail") || !strings.Contains(got, "INFO  visible event") {
		t.Fatalf("unexpected filtered log output %q", got)
	}
	if strings.Contains(got, "source=") {
		t.Fatalf("normal log unexpectedly contains source: %q", got)
	}
}

func TestConcurrentLogsRemainSeparateLines(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(config.Logging{Level: "info", Format: "text"}, &output)
	if err != nil {
		t.Fatal(err)
	}

	const writers = 25
	var wait sync.WaitGroup
	for index := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			logger.Info("concurrent event", "writer", index)
		}()
	}
	wait.Wait()

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != writers {
		t.Fatalf("got %d complete lines, want %d: %q", len(lines), writers, output.String())
	}
	for _, line := range lines {
		if strings.Count(line, "concurrent event") != 1 {
			t.Fatalf("interleaved log line %q", line)
		}
	}
}

func TestJSONDebugLogsRetainMachineReadableFormatAndSource(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(config.Logging{Level: "debug", Format: "json"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("packet received", "packet_id", 12)

	got := output.String()
	for _, part := range []string{`"level":"DEBUG"`, `"msg":"packet received"`, `"source":`, `"packet_id":12`} {
		if !strings.Contains(got, part) {
			t.Fatalf("JSON debug log %q does not contain %q", got, part)
		}
	}
}

func TestNewRejectsUnsupportedSettings(t *testing.T) {
	for _, cfg := range []config.Logging{
		{Level: "trace", Format: "text"},
		{Level: "info", Format: "xml"},
	} {
		if _, err := New(cfg, &bytes.Buffer{}); err == nil {
			t.Fatalf("New(%+v) accepted unsupported settings", cfg)
		}
	}
}
