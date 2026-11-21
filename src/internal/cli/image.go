package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/fileutil"
)

func imagePython(root string, environments ...string) (string, error) {
	if configured := os.Getenv("LOCALIZATION_KIT_IMAGE_PYTHON"); configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("LOCALIZATION_KIT_IMAGE_PYTHON: %w", err)
		}
		return filepath.Abs(path)
	}
	for _, name := range environments {
		env := filepath.Join(root, ".pixi", "envs", name)
		for _, path := range []string{filepath.Join(env, "python.exe"), filepath.Join(env, "bin", "python"), filepath.Join(env, "python")} {
			if found, err := exec.LookPath(path); err == nil {
				return filepath.Abs(found)
			}
		}
		if os.Getenv("PIXI_ENVIRONMENT_NAME") == name {
			if path, err := exec.LookPath("python"); err == nil {
				return filepath.Abs(path)
			}
		}
	}
	return "", fmt.Errorf("image Python environment is missing: run pixi install -e %s or set LOCALIZATION_KIT_IMAGE_PYTHON", environments[0])
}

type imageInvocation struct {
	args, environments []string
}

type imageUsageError string

func (e imageUsageError) Error() string { return string(e) }

func imageArgs(args []string) (*imageInvocation, error) {
	if len(args) == 0 {
		return nil, imageUsageError("image command is required")
	}
	flags := flag.NewFlagSet("image "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	type pathFlag struct {
		name          string
		dir, optional bool
	}
	translations := pathFlag{name: "translations", optional: true}
	workDir := pathFlag{name: "work-dir", dir: true}
	project := pathFlag{name: "project", dir: true, optional: true}
	var pathFlags []pathFlag
	var ports common.StringList
	var output, inpaint, pageID, groupBy, exportPath string
	var readback bool
	switch args[0] {
	case "ingest":
		pathFlags = []pathFlag{{name: "source"}, {name: "project", dir: true}}
	case "prepare":
		pathFlags = []pathFlag{{name: "image", optional: true}, project}
		flags.StringVar(&output, "out-dir", "", "")
		flags.StringVar(&pageID, "page-id", "c01/p001", "")
		flags.Bool("no-ogkalu", false, "")
		flags.Bool("redetect", false, "")
		flags.String("pages", "", "")
	case "render", "ocr", "export-text":
		workDir.optional = true
		pathFlags = []pathFlag{workDir, project}
		flags.String("pages", "", "")
		switch args[0] {
		case "render":
			pathFlags = append(pathFlags, translations, pathFlag{name: "baseline", optional: true})
			flags.StringVar(&output, "out-dir", "", "")
			flags.StringVar(&inpaint, "inpaint", "cpu", "")
			flags.BoolVar(&readback, "readback", false, "")
		case "ocr":
			flags.Bool("force", false, "")
		case "export-text":
			flags.StringVar(&exportPath, "out", "", "")
			flags.StringVar(&groupBy, "group-by", "chapter", "")
		}
	case "fingerprint":
		pathFlags = []pathFlag{workDir, {name: "rendered"}, translations}
	case "review-serve":
		pathFlags = []pathFlag{{name: "dir", dir: true, optional: true}, {name: "root", dir: true, optional: true}, {name: "web-dist", dir: true, optional: true}}
		flags.String("host", "", "")
		// Let argparse validate every occurrence, including leading zeros.
		flags.Var(&ports, "port", "")
	default:
		return nil, imageUsageError(fmt.Sprintf("unknown image command %q", args[0]))
	}
	for _, option := range pathFlags {
		flags.String(option.name, "", "")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return nil, imageUsageError(err.Error())
	}
	if flags.NArg() != 0 {
		return nil, imageUsageError(fmt.Sprintf("unexpected image argument %q", flags.Arg(0)))
	}
	if args[0] == "render" && inpaint != "cpu" && inpaint != "auto" && inpaint != "lama" {
		return nil, imageUsageError("--inpaint must be cpu, auto, or lama")
	}
	if args[0] == "prepare" && pageID == "" {
		return nil, imageUsageError("--page-id requires a page ID")
	}
	explicit := map[string]bool{}
	flags.Visit(func(flag *flag.Flag) { explicit[flag.Name] = true })
	proj := explicit["project"]
	switch args[0] {
	case "prepare":
		if proj == explicit["image"] || (proj && (explicit["out-dir"] || explicit["page-id"])) || (!proj && explicit["redetect"]) {
			return nil, imageUsageError("prepare requires --image with --out-dir, or --project")
		}
	case "render", "ocr", "export-text":
		if proj == explicit["work-dir"] || (proj && explicit["baseline"]) {
			return nil, imageUsageError("select one of --work-dir or --project (--baseline requires --work-dir)")
		}
	}
	if explicit["pages"] && (!proj || strings.TrimSpace(flags.Lookup("pages").Value.String()) == "") {
		return nil, imageUsageError("--pages requires --project and a nonempty page selection")
	}
	if args[0] == "export-text" && (exportPath == "" || (groupBy != "chapter" && groupBy != "page")) {
		return nil, imageUsageError("export-text requires --out and supports --group-by chapter or page")
	}
	normalized := []string{"-m", "src.image.cli", args[0]}
	var work string
	var inputs []string
	for _, option := range pathFlags {
		name := option.name
		if option.optional && !explicit[name] {
			continue
		}
		value := flags.Lookup(name).Value.String()
		if value == "" {
			return nil, imageUsageError(fmt.Sprintf("--%s requires a path", name))
		}
		path, err := fileutil.Canonical(value)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", name, err)
		}
		info, err := os.Stat(path)
		if args[0] == "ingest" && name == "project" && os.IsNotExist(err) {
			normalized = append(normalized, "--"+name, path)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", name, err)
		}
		if (option.dir && !info.IsDir()) || (!option.dir && !info.Mode().IsRegular() && !(name == "source" && info.IsDir())) {
			return nil, fmt.Errorf("--%s has the wrong path type: %s", name, path)
		}
		normalized = append(normalized, "--"+name, path)
		if name == "work-dir" {
			work = path
		} else if name == "project" {
			for _, entry := range []string{"manifest.json", "pages", "cache"} {
				inputs = append(inputs, filepath.Join(path, entry))
			}
		} else {
			inputs = append(inputs, path)
		}
	}

	if args[0] == "render" || (args[0] == "prepare" && !proj) {
		path := output
		if path == "" {
			if args[0] == "prepare" || proj || explicit["out-dir"] {
				return nil, imageUsageError("--out-dir requires a path")
			}
			path = filepath.Join(work, "render")
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if work != "" {
			for _, name := range []string{"analysis.json", "overrides.toml", "translations.jsonl"} {
				inputs = append(inputs, filepath.Join(work, name))
			}
		}
		if err := fileutil.CheckNewPath(path, inputs...); err != nil {
			return nil, err
		}
		normalized = append(normalized, "--out-dir", path)
	}
	if exportPath != "" {
		path, err := filepath.Abs(exportPath)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, "--out", path)
	}
	for _, name := range []string{"page-id", "host", "pages", "group-by"} {
		if explicit[name] {
			value := flags.Lookup(name).Value.String()
			if strings.HasPrefix(value, "-") {
				normalized = append(normalized, "--"+name+"="+value)
			} else {
				normalized = append(normalized, "--"+name, value)
			}
		}
	}
	for _, port := range ports {
		normalized = append(normalized, "--port="+port)
	}
	for _, name := range []string{"no-ogkalu", "redetect", "force", "readback"} {
		if explicit[name] && flags.Lookup(name).Value.String() == "true" {
			normalized = append(normalized, "--"+name)
		}
	}
	if explicit["inpaint"] {
		normalized = append(normalized, "--inpaint="+inpaint)
	}
	return &imageInvocation{normalized, imageEnvironments(args[0], inpaint, readback)}, nil
}

func imageEnvironments(command, inpaint string, readback bool) []string {
	switch {
	case command == "prepare" || command == "ocr" || inpaint == "lama" || readback:
		return []string{"image-gpu"}
	case inpaint == "auto":
		return []string{"image-gpu", "image"}
	}
	return []string{"image", "image-gpu"}
}

func runImage(ctx context.Context, args []string, root string, stdout, stderr io.Writer) error {
	invocation, err := imageArgs(args)
	if err != nil {
		return err
	}
	python, err := imagePython(root, invocation.environments...)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, python, invocation.args...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(cmd.Environ(), "PYTHONUTF8=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("image %s: %w", args[0], err)
	}
	return nil
}
