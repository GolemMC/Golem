// SPDX-License-Identifier: AGPL-3.0-only

// Package logging constructs Golem's human-readable console and JSON loggers.
package logging

import (
	"context"
	"encoding"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/GolemMC/Golem/internal/config"
)

func New(cfg config.Logging, out io.Writer) (*slog.Logger, error) {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	level, ok := levels[strings.ToLower(cfg.Level)]
	if !ok {
		return nil, fmt.Errorf("unsupported log level %q", cfg.Level)
	}
	opts := &slog.HandlerOptions{Level: level, AddSource: level == slog.LevelDebug}
	switch strings.ToLower(cfg.Format) {
	case "json":
		return slog.New(slog.NewJSONHandler(out, opts)), nil
	case "text":
		return slog.New(newConsoleHandler(out, level, level == slog.LevelDebug)), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q", cfg.Format)
	}
}

type consoleHandler struct {
	out       io.Writer
	level     slog.Level
	addSource bool
	color     bool
	mu        *sync.Mutex
	attrs     []boundAttr
	groups    []string
}

type boundAttr struct {
	groups []string
	attr   slog.Attr
}

func newConsoleHandler(out io.Writer, level slog.Level, addSource bool) *consoleHandler {
	return &consoleHandler{
		out:       out,
		level:     level,
		addSource: addSource,
		color:     supportsColor(out),
		mu:        &sync.Mutex{},
	}
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *consoleHandler) Handle(_ context.Context, record slog.Record) error {
	var output strings.Builder
	timestamp := record.Time.Local().Format("2006-01-02 15:04:05")
	if h.addSource {
		timestamp = record.Time.Local().Format("2006-01-02 15:04:05.000")
	}
	if record.Time.IsZero() {
		timestamp = strings.Repeat("-", len(timestamp))
	}

	if h.color {
		output.WriteString("\x1b[2m[")
		output.WriteString(timestamp)
		output.WriteString("]\x1b[0m ")
		output.WriteString(levelColor(record.Level))
		output.WriteString(levelLabel(record.Level))
		output.WriteString("\x1b[0m ")
	} else {
		fmt.Fprintf(&output, "[%s] %s ", timestamp, levelLabel(record.Level))
	}
	output.WriteString(record.Message)

	attrs := make([]boundAttr, 0, len(h.attrs)+record.NumAttrs()+1)
	attrs = append(attrs, h.attrs...)
	if h.addSource && record.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
		attrs = append(attrs, boundAttr{attr: slog.String("source", shortSource(frame.File, frame.Line))})
	}
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, boundAttr{groups: h.groups, attr: attr})
		return true
	})
	for _, attr := range attrs {
		appendAttr(&output, attr.groups, attr.attr)
	}
	output.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, output.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = slices.Clone(h.attrs)
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, boundAttr{groups: slices.Clone(h.groups), attr: attr})
	}
	return &clone
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(slices.Clone(h.groups), name)
	return &clone
}

func appendAttr(output *strings.Builder, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			groups = append(slices.Clone(groups), attr.Key)
		}
		for _, child := range attr.Value.Group() {
			appendAttr(output, groups, child)
		}
		return
	}
	key := strings.Join(append(slices.Clone(groups), attr.Key), ".")
	if key == "" {
		return
	}
	output.WriteByte(' ')
	output.WriteString(key)
	output.WriteByte('=')
	output.WriteString(formatValue(attr.Value))
}

func formatValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return quoteWhenNeeded(value.String())
	case slog.KindInt64:
		return strconv.FormatInt(value.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(value.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(value.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.FormatBool(value.Bool())
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	case slog.KindAny:
		if value.Any() == nil {
			return "<nil>"
		}
		if text, ok := value.Any().(encoding.TextMarshaler); ok {
			if marshaled, err := text.MarshalText(); err == nil {
				return quoteWhenNeeded(string(marshaled))
			}
		}
		return quoteWhenNeeded(fmt.Sprint(value.Any()))
	default:
		return quoteWhenNeeded(value.String())
	}
}

func quoteWhenNeeded(value string) string {
	if value == "" || strings.ContainsRune(value, '=') || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

func levelLabel(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO "
	case level < slog.LevelError:
		return "WARN "
	default:
		return "ERROR"
	}
}

func levelColor(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "\x1b[36m"
	case level < slog.LevelWarn:
		return "\x1b[32m"
	case level < slog.LevelError:
		return "\x1b[33m"
	default:
		return "\x1b[31m"
	}
}

func shortSource(file string, line int) string {
	path := filepath.ToSlash(file)
	for _, marker := range []string{"/internal/", "/cmd/"} {
		if index := strings.LastIndex(path, marker); index >= 0 {
			return fmt.Sprintf("%s:%d", path[index+1:], line)
		}
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

func supportsColor(out io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
