package common

import (
	"strings"
	"time"
)

const (
	DefaultTaskTTL          = 30 * time.Minute
	DefaultTaskContext      = 5
	DefaultTaskMaxLines     = 20
	DefaultTaskScope        = "group"
	DefaultQualityMaxOutput = 2048
)

var ResourceRoot = "."

type StringList []string

func (p *StringList) String() string { return strings.Join(*p, ", ") }
func (p *StringList) Set(value string) error {
	*p = append(*p, value)
	return nil
}
