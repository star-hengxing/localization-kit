//go:build !windows && !linux

package fileutil

import "fmt"

func PublishDirectory(source, target string) error {
	return fmt.Errorf("此平台尚不支持安全发布目录")
}
