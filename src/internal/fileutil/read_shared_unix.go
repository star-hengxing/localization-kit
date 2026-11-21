//go:build !windows

package fileutil

import "os"

func ReadFileShared(path string) ([]byte, error) { return os.ReadFile(path) }
