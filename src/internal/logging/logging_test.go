package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"
	"time"
)

func TestLogOutputFormat(t *testing.T) {
	var output bytes.Buffer
	handler := newLogHandler(&output, slog.LevelInfo)
	when := time.Date(2026, 11, 16, 23, 13, 52, 90*int(time.Millisecond), time.Local)
	record := slog.NewRecord(when, slog.LevelInfo, "开始翻译", 0)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "INFO  2026-11-16 23:13:52.090  开始翻译\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFinalErrorIsFormattedAndOneLine(t *testing.T) {
	var output bytes.Buffer
	WriteFinalError(&output, errors.New("输入错误\n细节"))
	if !regexp.MustCompile(`^ERROR \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}  输入错误\\n细节\n$`).MatchString(output.String()) {
		t.Fatalf("output=%q", output.String())
	}
}

func TestLogFieldsAndGroups(t *testing.T) {
	var output bytes.Buffer
	handler := newLogHandler(&output, slog.LevelInfo).WithAttrs([]slog.Attr{slog.String("job", "one")}).WithGroup("model")
	record := slog.NewRecord(time.Date(2026, 11, 16, 23, 13, 52, 0, time.Local), slog.LevelWarn, "重试", 0)
	record.AddAttrs(slog.String("reason", "service unavailable"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "WARN  2026-11-16 23:13:52.000  重试 job=one model.reason=\"service unavailable\"\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLogFieldKeysStayOnOneLine(t *testing.T) {
	var output bytes.Buffer
	handler := newLogHandler(&output, slog.LevelInfo).WithGroup("a\nb")
	record := slog.NewRecord(time.Date(2026, 11, 16, 23, 13, 52, 0, time.Local), slog.LevelInfo, "消息", 0)
	record.AddAttrs(slog.Attr{}, slog.String("", "value"), slog.String("key\nname", "value"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "INFO  2026-11-16 23:13:52.000  消息 a\\nb.=value a\\nb.key\\nname=value\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLogLevelControlsHandler(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	ConfigureLogger("warn")
	if slog.Default().Enabled(context.Background(), slog.LevelInfo) || !slog.Default().Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("warn level not applied")
	}
	ConfigureLogger("debug")
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug level not applied")
	}
}
