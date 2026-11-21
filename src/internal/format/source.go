package format

import (
	"fmt"
	"localization-kit/src/internal/document"
	"localization-kit/src/internal/format/artc"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/format/vtt"
)

type Source struct {
	Adapter  string
	Input    string
	Segments []document.Segment
	Tracks   []vtt.Track
	Book     artc.Book
}

func LoadSource(adapter, input string) (Source, error) {
	source := Source{Adapter: adapter, Input: input}
	var err error
	switch adapter {
	case "jsonl":
		source.Segments, err = jsonl.LoadJSONL(input)
	case "vtt":
		source.Tracks, source.Segments, err = vtt.LoadVTT(input)
	case "artc":
		source.Book, source.Segments, err = artc.LoadArtc(input)
	default:
		err = fmt.Errorf("不支持输入格式 %q", adapter)
	}
	return source, err
}
