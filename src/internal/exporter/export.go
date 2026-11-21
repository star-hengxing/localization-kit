package exporter

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/format"
	"localization-kit/src/internal/format/artc"
	"localization-kit/src/internal/format/jsonl"
	"localization-kit/src/internal/format/vtt"
	"localization-kit/src/internal/translate/rules"
)

// Publish requires completed results for the selected stage.
func Publish(source format.Source, results map[string]document.Result, output string, entries []rules.Entry, credits []string, bilingual bool) error {
	if err := verifySource(source); err != nil {
		return err
	}
	if err := fileutil.CheckOutput(output); err != nil {
		return err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if source.Adapter == "jsonl" {
		f, err := os.CreateTemp(parent, ".localization-export-*.jsonl")
		if err != nil {
			return err
		}
		stage := f.Name()
		defer os.Remove(stage)
		writeErr := jsonl.ExportJSONL(f, source.Segments, results, nil)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := verifySource(source); err != nil {
			return err
		}
		// A hard link publishes only if output does not already exist.
		if err := os.Link(stage, output); err != nil {
			return fmt.Errorf("发布输出: %w", err)
		}
		return nil
	}
	stage, err := os.MkdirTemp(parent, ".localization-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var lines map[string]document.ExportLine
	if source.Adapter == "vtt" {
		err = vtt.ExportVTT(source.Tracks, results, stage, entries, credits)
	} else {
		lines = make(map[string]document.ExportLine)
		err = artc.ExportArtc(source.Book, results, stage, entries, bilingual, credits, lines)
	}
	if err != nil {
		return err
	}
	if lines != nil {
		if err := checkArtcOutput(source.Book, stage, entries, bilingual, lines); err != nil {
			return err
		}
	}
	if err := verifySource(source); err != nil {
		return err
	}
	if err := fileutil.CheckOutput(output); err != nil {
		return err
	}
	if err := fileutil.PublishDirectory(stage, output); err != nil {
		return fmt.Errorf("发布输出: %w", err)
	}
	return nil
}

func checkArtcOutput(book artc.Book, output string, entries []rules.Entry, bilingual bool, lines map[string]document.ExportLine) error {
	for _, id := range slices.Sorted(maps.Keys(lines)) {
		line := lines[id]
		_, warnings := rules.Check(line.Src, line.Dst, entries)
		for _, issue := range warnings {
			if issue.Code == "traditional" || issue.Code == "latin" {
				return fmt.Errorf("小说导出条目 %s 未通过字形或英文检查 (%s): %s", id, issue.Code, issue.Detail)
			}
		}
	}
	if bilingual {
		for _, chapter := range book.Chapters {
			if chapter.Verbatim {
				continue
			}
			data, err := os.ReadFile(filepath.Join(output, "original", filepath.FromSlash(chapter.Group)+".txt"))
			if err != nil {
				return fmt.Errorf("读取双语原文章节 %s: %w", chapter.Group, err)
			}
			if !bytes.Equal(data, chapter.Data) {
				return fmt.Errorf("双语原文章节不一致: %s", chapter.Group)
			}
		}
	}
	return nil
}

func verifySource(source format.Source) error {
	current, err := format.LoadSource(source.Adapter, source.Input)
	if err != nil {
		return fmt.Errorf("重新检查输入: %w", err)
	}
	if !reflect.DeepEqual(source, current) {
		return fmt.Errorf("输入在处理期间发生变化，请检查后重新运行")
	}
	return nil
}
