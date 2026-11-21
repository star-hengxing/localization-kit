package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	base := abs
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(base)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(base)
		if parent == base {
			return "", err
		}
		missing = append(missing, filepath.Base(base))
		base = parent
	}
}

func Overlaps(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	rel, err := filepath.Rel(a, b)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func CheckOutput(output string) error {
	_, err := os.Lstat(output)
	if err == nil {
		return fmt.Errorf("输出路径已存在，请使用新路径: %s", output)
	}
	if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func CheckPaths(input, job, output string) error {
	inAbs, err := Canonical(input)
	if err != nil {
		return err
	}
	jobAbs, err := Canonical(job)
	if err != nil {
		return err
	}
	outAbs, err := Canonical(output)
	if err != nil {
		return err
	}
	if Overlaps(inAbs, outAbs) || Overlaps(jobAbs, outAbs) || Overlaps(inAbs, jobAbs) {
		return fmt.Errorf("输入、作品目录与输出路径不能互相重叠")
	}
	return nil
}

func CheckNewPath(path string, others ...string) error {
	if path == "" {
		return nil
	}
	abs, err := Canonical(path)
	if err != nil {
		return err
	}
	for _, other := range others {
		otherAbs, err := Canonical(other)
		if err != nil {
			return err
		}
		if Overlaps(abs, otherAbs) {
			return fmt.Errorf("新文件路径不能与现有输入路径重叠")
		}
	}
	return CheckOutput(path)
}
