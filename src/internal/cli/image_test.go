package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"localization-kit/src/internal/fileutil"
)

func imageTestChild() int {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "image subprocess stderr")
	if err := json.NewEncoder(os.Stdout).Encode(imageChildResult{Args: os.Args[1:], Dir: dir}); err != nil {
		return 1
	}
	if os.Getenv("LOCALIZATION_KIT_IMAGE_TEST_CHILD") == "fail" {
		return 23
	}
	return 0
}

type imageChildResult struct {
	Args []string
	Dir  string
}

func setupImageChild(t *testing.T) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALIZATION_KIT_IMAGE_PYTHON", binary)
	t.Setenv("LOCALIZATION_KIT_IMAGE_TEST_CHILD", "pass")
}

func TestImageBridge(t *testing.T) {
	setupImageChild(t)
	root, err := fileutil.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "image work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	translations := filepath.Join(work, "translations sample.jsonl")
	if err := os.WriteFile(translations, nil, 0600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(work, "baseline sample.png")
	if err := os.WriteFile(baseline, nil, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"render", "--work-dir=" + work, "--translations", translations, "--baseline", baseline, "--inpaint", "lama"}
	var stdout, stderr bytes.Buffer
	if err := runImage(context.Background(), args, root, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var result imageChildResult
	expected := []string{"-m", "src.image.cli", "render", "--work-dir", work, "--translations", translations, "--baseline", baseline, "--out-dir", filepath.Join(work, "render"), "--inpaint=lama"}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Dir != root || !reflect.DeepEqual(result.Args, expected) {
		t.Fatalf("child result: %+v, err: %v", result, err)
	}
	if !strings.Contains(stderr.String(), "image subprocess stderr") {
		t.Fatalf("stderr = %q", &stderr)
	}

	out := filepath.Join(work, "render")
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(out, "existing.txt")
	if err := os.WriteFile(marker, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runImage(context.Background(), args, root, &stdout, &stderr); err == nil || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("existing output reached subprocess: err=%v stdout=%q stderr=%q", err, &stdout, &stderr)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "existing" {
		t.Fatalf("existing output changed: %q, %v", data, err)
	}
	if _, err := imageArgs([]string{"render", "--work-dir", translations}); err == nil {
		t.Fatal("accepted a file as work directory")
	}
	if _, err := imageArgs([]string{"render", "--work-dir", work, "--baseline", work}); err == nil {
		t.Fatal("accepted a directory as baseline")
	}
	if _, err := imageArgs([]string{"render", "--work-dir", work, "--inpaint", "gpu"}); err == nil || !strings.Contains(err.Error(), "--inpaint") {
		t.Fatalf("invalid inpaint error = %v", err)
	}

	t.Setenv("LOCALIZATION_KIT_IMAGE_TEST_CHILD", "fail")
	err = runImage(context.Background(), append(args, "--out-dir", filepath.Join(work, "new")), root, &stdout, &stderr)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || !strings.Contains(stderr.String(), "image subprocess stderr") {
		t.Fatalf("child failure: %v stderr=%q", err, &stderr)
	}
}

func TestImagePrepareBridge(t *testing.T) {
	setupImageChild(t)
	root, err := fileutil.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "source image.png")
	if err := os.WriteFile(original, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "next work")
	args := []string{"prepare", "--image", original, "--out-dir", out, "--page-id", "c02/p004", "--no-ogkalu"}
	var stdout bytes.Buffer
	if err := runImage(context.Background(), args, root, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result imageChildResult
	want := append([]string{"-m", "src.image.cli"}, args...)
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("prepare result = %+v, err = %v", result, err)
	}
	for _, args := range [][]string{
		{"prepare", "--image", original},
		{"prepare", "--image", root, "--out-dir", out},
		{"prepare", "--image", original, "--out-dir", original},
		{"prepare", "--image", original, "--out-dir", filepath.Join(original, "child")},
		{"prepare", "--image", original, "--out-dir", out, "--page-id="},
	} {
		if _, err := imageArgs(args); err == nil {
			t.Errorf("accepted invalid prepare arguments: %v", args)
		}
	}
	if err := runImage(context.Background(), []string{"import-legacy"}, root, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unknown image command") {
		t.Fatalf("removed command error = %v", err)
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != "source" {
		t.Fatalf("source changed: %q, %v", data, err)
	}
}

func TestImagePythonEnvironments(t *testing.T) {
	t.Setenv("LOCALIZATION_KIT_IMAGE_PYTHON", "")
	t.Setenv("PIXI_ENVIRONMENT_NAME", "")
	root := t.TempDir()
	if _, err := imagePython(root, "image-gpu"); err == nil || !strings.Contains(err.Error(), "pixi install -e image-gpu") {
		t.Fatalf("missing GPU environment error = %v", err)
	}
	python := func(environment string) string {
		path := filepath.Join(root, ".pixi", "envs", environment, "python.exe")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	check := func(want string, environments ...string) {
		t.Helper()
		if got, err := imagePython(root, environments...); err != nil || got != want {
			t.Fatalf("Python for %v = %q, err = %v", environments, got, err)
		}
	}
	gpu := python("image-gpu")
	check(gpu, "image", "image-gpu")
	cpu := python("image")
	check(cpu, "image", "image-gpu")
	check(gpu, "image-gpu", "image")
	t.Setenv("LOCALIZATION_KIT_IMAGE_PYTHON", cpu)
	check(cpu, "image-gpu")
}

func TestImageReviewServeArgs(t *testing.T) {
	root, err := fileutil.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"review-serve", "--dir", file},
		{"review-serve", "--root", file},
		{"review-serve", "--dir", root, "--web-dist", file},
		{"review-serve", "--dir", root, "--web-dist="},
		{"review-serve", "--dir", root, "--out-dir", root},
	} {
		if _, err := imageArgs(args); err == nil {
			t.Errorf("accepted invalid arguments: %v", args)
		}
	}
	defaults, err := imageArgs([]string{"review-serve", "--dir=" + root})
	want := []string{"-m", "src.image.cli", "review-serve", "--dir", root}
	if err != nil || !slices.Equal(defaults.args, want) {
		t.Fatalf("default review arguments = %v, err = %v", defaults, err)
	}
	normalized, err := imageArgs([]string{"review-serve", "--dir", root, "--port=0", "--host", "localhost", "--web-dist", root})
	want = append(want, "--web-dist", root, "--host", "localhost", "--port=0")
	if err != nil || !slices.Equal(normalized.args, want) {
		t.Fatalf("review arguments = %v, err = %v", normalized, err)
	}
	decimal, err := imageArgs([]string{"review-serve", "--port=0", "--port", "08080"})
	if err != nil || !slices.Equal(decimal.args, []string{"-m", "src.image.cli", "review-serve", "--port=0", "--port=08080"}) {
		t.Fatalf("decimal port arguments = %v, err = %v", decimal, err)
	}
	for _, args := range [][]string{{"review-serve"}, {"review-serve", "--root", root, "--host=--readback"}} {
		invocation, err := imageArgs(args)
		if err != nil || invocation.environments[0] != "image" ||
			slices.Contains(args, "--host=--readback") != slices.Contains(invocation.args, "--host=--readback") {
			t.Fatalf("%v: got %v, %v", args, invocation, err)
		}
	}
}

func TestImageBinaryPreservesChildExitCode(t *testing.T) {
	binary := os.Getenv("LOCALIZATION_KIT_TEST_BINARY")
	if binary == "" {
		t.Skip("LOCALIZATION_KIT_TEST_BINARY is not configured")
	}
	setupImageChild(t)
	t.Setenv("LOCALIZATION_KIT_IMAGE_TEST_CHILD", "fail")
	work := t.TempDir()
	rendered := filepath.Join(work, "rendered.png")
	if err := os.WriteFile(rendered, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "image", "fingerprint", "--work-dir", work, "--rendered", rendered)
	cmd.Dir = work
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || !json.Valid(stdout) ||
		strings.TrimSpace(stderr.String()) != "image subprocess stderr" {
		t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout, &stderr)
	}
	for _, args := range [][]string{
		{"render", "--work-dir", work, "--inpaint", "invalid"},
		{"ocr", "--work-dir", work, "--pages", "c01/p001"},
		{"render", "--project", work},
	} {
		err := exec.Command(binary, append([]string{"image"}, args...)...).Run()
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Errorf("%v: expected usage exit 2, got %v", args, err)
		}
	}
}

func TestImageProjectCommands(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "new")
	for _, c := range []struct {
		args   []string
		envs   string
		option string
	}{
		{[]string{"ingest", "--source", root, "--project", out}, "image,image-gpu", "--source"},
		{[]string{"prepare", "--project", root, "--redetect"}, "image-gpu", "--redetect"},
		{[]string{"ocr", "--project", root, "--pages", "c01/p002", "--force"}, "image-gpu", "--force"},
		{[]string{"export-text", "--project", root, "--out", out, "--group-by", "page"}, "image,image-gpu", "--group-by"},
		{[]string{"render", "--project", root, "--out-dir", out, "--inpaint", "auto"}, "image-gpu,image", "--inpaint=auto"},
		{[]string{"render", "--project", root, "--out-dir", out, "--readback"}, "image-gpu", "--readback"},
	} {
		args, err := imageArgs(c.args)
		if err != nil || !slices.Contains(args.args, c.option) || strings.Join(args.environments, ",") != c.envs {
			t.Fatalf("%v: normalized %v, error %v", c.args, args, err)
		}
		if !strings.Contains(commandUsage["image "+c.args[0]], "--project") {
			t.Errorf("project usage missing for %s", c.args[0])
		}
	}
	for _, args := range [][]string{
		{"ingest", "--source", out, "--project", root},
		{"prepare", "--project", root, "--image", out},
		{"ocr", "--work-dir", root, "--pages", "c01/p001"},
		{"export-text", "--project", root, "--out", out, "--group-by", "volume"},
		{"render", "--project", root},
	} {
		if _, err := imageArgs(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
