package main

import (
	"archive/zip"
	"debug/buildinfo"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/pelletier/go-toml/v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	defaultBinary := "build/localization-kit"
	if runtime.GOOS == "windows" {
		defaultBinary += ".exe"
	}
	binary := flag.String("binary", defaultBinary, "要打包的 Go 可执行文件")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("不接受额外的位置参数")
	}
	info, err := buildinfo.ReadFile(*binary)
	if err != nil {
		return fmt.Errorf("读取可执行文件构建信息: %w", err)
	}
	var targetOS, targetArch string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			targetOS = setting.Value
		case "GOARCH":
			targetArch = setting.Value
		}
	}
	if targetOS == "" || targetArch == "" {
		return fmt.Errorf("可执行文件缺少目标平台信息")
	}
	data, err := os.ReadFile("pixi.toml")
	if err != nil {
		return err
	}
	var config struct {
		Workspace struct{ Version string }
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return err
	}
	version := config.Workspace.Version
	if version == "" || filepath.Base(version) != version {
		return fmt.Errorf("pixi.toml 的版本号无效")
	}
	if err := os.MkdirAll("build", 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp("build", "localization-kit-*.zip.pending")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	archive := zip.NewWriter(file)
	add := func(source, target string, executable bool) error {
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("打包文件不是普通文件: %s", source)
		}
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = "localization-kit/" + filepath.ToSlash(target)
		header.Method = zip.Deflate
		if executable && targetOS != "windows" {
			header.SetMode(0755)
		}
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(entry, input)
		return err
	}
	name := "localization-kit"
	if targetOS == "windows" {
		name += ".exe"
	}
	if err := add(*binary, name, true); err != nil {
		file.Close()
		return err
	}
	if err := add("LICENSE", "LICENSE", false); err != nil {
		file.Close()
		return err
	}
	dirs := []struct {
		src string
		dst string
	}{
		{src: "data", dst: "data"},
		{src: filepath.Join("packaging", "licenses"), dst: "licenses"},
		{src: "schemas", dst: "schemas"},
	}
	for _, dir := range dirs {
		if err := filepath.WalkDir(dir.src, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir.src, path)
			if err != nil {
				return err
			}
			return add(path, filepath.Join(dir.dst, rel), false)
		}); err != nil {
			file.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	output := filepath.Join("build", "localization-kit-"+version+"-"+targetOS+"-"+targetArch+".zip")
	if err := os.Rename(file.Name(), output); err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}
