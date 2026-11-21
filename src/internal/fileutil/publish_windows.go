//go:build windows

package fileutil

import "os"

func PublishDirectory(source, target string) error { return os.Rename(source, target) }
