package applog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	colorReset  = "\x1b[0m"
	colorDim    = "\x1b[2m"
	colorDebug  = "\x1b[36m"
	colorInfo   = "\x1b[32m"
	colorWarn   = "\x1b[33m"
	colorError  = "\x1b[31m"
	timeLayout  = "15:04:05.000"
	defaultTerm = "dumb"
)

type consoleHandlerOptions struct {
	Level slog.Leveler
	Color bool
}

type consoleHandler struct {
	out    io.Writer
	opts   consoleHandlerOptions
	mu     *sync.Mutex
	attrs  []slog.Attr
	groups []string
}

func newConsoleHandler(out io.Writer, opts consoleHandlerOptions) slog.Handler {
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}

	return &consoleHandler{
		out:  out,
		opts: opts,
		mu:   &sync.Mutex{},
	}
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *consoleHandler) Handle(_ context.Context, record slog.Record) error {
	var buf bytes.Buffer
	h.appendHeader(&buf, record)

	for _, attr := range h.attrs {
		h.appendAttr(&buf, h.groups, attr)
	}

	record.Attrs(func(attr slog.Attr) bool {
		h.appendAttr(&buf, h.groups, attr)

		return true
	})
	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := h.out.Write(buf.Bytes())

	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := h.clone()
	next.attrs = append(next.attrs, attrs...)

	return next
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	next := h.clone()
	next.groups = append(next.groups, name)

	return next
}

func (h *consoleHandler) clone() *consoleHandler {
	next := *h
	next.attrs = append([]slog.Attr(nil), h.attrs...)
	next.groups = append([]string(nil), h.groups...)

	return &next
}

func (h *consoleHandler) appendHeader(buf *bytes.Buffer, record slog.Record) {
	t := record.Time
	if t.IsZero() {
		t = time.Now()
	}

	h.writeDim(buf, t.Format(timeLayout))
	buf.WriteByte(' ')
	h.writeLevel(buf, record.Level)
	buf.WriteByte(' ')
	buf.WriteString(record.Message)
}

func (h *consoleHandler) appendAttr(buf *bytes.Buffer, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}

	if attr.Value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			groups = append(groups, attr.Key)
		}

		for _, nested := range attr.Value.Group() {
			h.appendAttr(buf, groups, nested)
		}

		return
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(append(append([]string(nil), groups...), key), ".")
	}

	if key == "" {
		return
	}

	buf.WriteByte(' ')
	h.writeDim(buf, key)
	buf.WriteByte('=')
	buf.WriteString(formatValue(attr.Value))
}

func (h *consoleHandler) writeLevel(buf *bytes.Buffer, level slog.Level) {
	label, color := levelLabel(level)
	if h.opts.Color {
		buf.WriteString(color)
		buf.WriteString(label)
		buf.WriteString(colorReset)

		return
	}

	buf.WriteString(label)
}

func (h *consoleHandler) writeDim(buf *bytes.Buffer, s string) {
	if h.opts.Color {
		buf.WriteString(colorDim)
		buf.WriteString(s)
		buf.WriteString(colorReset)

		return
	}

	buf.WriteString(s)
}

func levelLabel(level slog.Level) (string, string) {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG", colorDebug
	case level < slog.LevelWarn:
		return "INFO ", colorInfo
	case level < slog.LevelError:
		return "WARN ", colorWarn
	default:
		return "ERROR", colorError
	}
}

func formatValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return quoteIfNeeded(value.String())
	case slog.KindBool:
		return strconv.FormatBool(value.Bool())
	case slog.KindInt64:
		return strconv.FormatInt(value.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(value.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(value.Float64(), 'f', -1, 64)
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339)
	case slog.KindAny:
		return quoteIfNeeded(fmt.Sprint(value.Any()))
	default:
		return quoteIfNeeded(value.String())
	}
}

func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\r\n\"=") {
		return strconv.Quote(s)
	}

	return s
}

func shouldColor(file *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}

	if os.Getenv("TERM") == defaultTerm {
		return false
	}

	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}
