package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/cli/evaluation"
	"localization-kit/src/internal/logging"
	"localization-kit/src/internal/store"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func Main() {
	logging.ConfigureLogger("info")
	executable, err := os.Executable()
	if err != nil {
		logging.WriteFinalError(os.Stderr, err)
		os.Exit(1)
	}
	common.ResourceRoot = filepath.Dir(executable)
	if filepath.Base(common.ResourceRoot) == "build" {
		parent := filepath.Dir(common.ResourceRoot)
		if _, err := os.Stat(filepath.Join(parent, "pixi.toml")); err == nil {
			common.ResourceRoot = parent
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	out := io.Writer(os.Stderr)
	var logFile *os.File
	var logOutput *workspaceLog
	command := ""
	started := time.Now()
	args, path, done, err := resolveCommand(os.Args[1:])
	if err != nil {
		logging.WriteFinalError(os.Stderr, err)
		os.Exit(1)
	}
	if done {
		return
	}
	if path != "" {
		command = os.Args[1]
		if isGroup(command) {
			command += " " + os.Args[2]
		}
		logFile, err = os.OpenFile(filepath.Join(path, "localization-kit.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			logging.WriteFinalError(os.Stderr, fmt.Errorf("创建作品日志: %w", err))
			os.Exit(1)
		}
		logOutput = &workspaceLog{file: logFile}
		out = logOutput
		logging.SetOutput(out)
		logging.ConfigureLogger("info")
		slog.Info("开始 " + command + " 作品=" + path)
	}
	runErr := runResolved(ctx, args, path)
	var exited *exec.ExitError
	if args[0] == "image" && errors.As(runErr, &exited) && exited.ExitCode() > 0 {
		os.Exit(exited.ExitCode())
	}
	if runErr != nil {
		if command != "" {
			runErr = fmt.Errorf("%s: %w", command, runErr)
		}
		logging.WriteFinalError(out, runErr)
	} else if logFile != nil {
		slog.Info(fmt.Sprintf("完成 %s 耗时=%s", command, time.Since(started).Round(time.Millisecond)))
	}
	if logFile != nil {
		if err := errors.Join(logOutput.err, logFile.Sync(), logFile.Close()); err != nil {
			logging.WriteFinalError(os.Stderr, fmt.Errorf("写入作品日志: %w", err))
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		var usage imageUsageError
		if errors.As(runErr, &usage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type workspaceLog struct {
	file *os.File
	err  error
}

func (w *workspaceLog) Write(data []byte) (int, error) {
	_, terminalErr := os.Stderr.Write(data)
	n, fileErr := w.file.Write(data)
	if fileErr == nil && n != len(data) {
		fileErr = io.ErrShortWrite
	}
	w.err = errors.Join(w.err, fileErr)
	return len(data), errors.Join(terminalErr, fileErr)
}

func run(ctx context.Context, args []string) error {
	args, path, done, err := resolveCommand(args)
	if err != nil || done {
		return err
	}
	return runResolved(ctx, args, path)
}

func runResolved(ctx context.Context, args []string, path string) error {
	if path != "" {
		waitForLock := args[0] == "next" || args[0] == "submit"
		deadline := time.Now().Add(2 * time.Second)
		for {
			handled, err := workspaceRun(ctx, args)
			if !handled {
				return fmt.Errorf("未实现的作品命令 %s", args[0])
			}
			if !waitForLock || !errors.Is(err, store.ErrLocked) || !time.Now().Before(deadline) {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	switch args[0] {
	case "eval":
		return evaluation.Run(args[1:])
	case "image":
		return runImage(ctx, args[1:], common.ResourceRoot, os.Stdout, os.Stderr)
	default:
		return fmt.Errorf("未实现的命令 %s", args[0])
	}
}
