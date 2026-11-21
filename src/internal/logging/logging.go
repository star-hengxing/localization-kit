package logging

import (
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

type logField struct {
	group string
	attr  slog.Attr
}

type logHandler struct {
	out    io.Writer
	level  slog.Level
	mu     *sync.Mutex
	group  string
	fields []logField
}

var output io.Writer = os.Stderr

func SetOutput(out io.Writer) { output = out }

func newLogHandler(out io.Writer, level slog.Level) *logHandler {
	return &logHandler{out: out, level: level, mu: &sync.Mutex{}}
}

func (h *logHandler) Enabled(_ context.Context, level slog.Level) bool { return level >= h.level }

func (h *logHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	fmt.Fprintf(&line, "%-5s %s  %s", record.Level.String(), record.Time.Format("2006-01-02 15:04:05.000"), logLine(record.Message))
	for _, field := range h.fields {
		appendLogField(&line, field.group, field.attr)
	}
	record.Attrs(func(attr slog.Attr) bool { appendLogField(&line, h.group, attr); return true })
	line.WriteByte('\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, line.String())
	return err
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.fields = append([]logField(nil), h.fields...)
	for _, attr := range attrs {
		clone.fields = append(clone.fields, logField{group: h.group, attr: attr})
	}
	return &clone
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.group = h.group + name + "."
	return &clone
}

func appendLogField(line *strings.Builder, group string, attr slog.Attr) {
	if attr.Equal(slog.Attr{}) {
		return
	}
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		next := group
		if attr.Key != "" {
			next += attr.Key + "."
		}
		for _, child := range value.Group() {
			appendLogField(line, next, child)
		}
		return
	}
	text := value.String()
	if strings.ContainsAny(text, " \t\r\n=\"") {
		text = strconv.Quote(text)
	}
	fmt.Fprintf(line, " %s=%s", logLine(group+attr.Key), text)
}

func logLine(value string) string {
	return strings.NewReplacer("\r", "\\r", "\n", "\\n").Replace(value)
}

func ConfigureLogger(value string) {
	var level slog.Level
	_ = level.UnmarshalText([]byte(value))
	slog.SetDefault(slog.New(newLogHandler(output, level)))
}

func WriteFinalError(out io.Writer, err error) {
	fmt.Fprintf(out, "ERROR %s  %s\n", time.Now().Format("2006-01-02 15:04:05.000"), logLine(err.Error()))
}
