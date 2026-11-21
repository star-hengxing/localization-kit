package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(path, []byte("[models.test]\nurl='http://127.0.0.1:8888'\nmodel='example'\nprofile='hy-mt2'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.SelectModel("test")
	if err != nil || got.Model != "example" || got.URL != "http://127.0.0.1:8888" {
		t.Fatalf("model: %+v; error: %v", got, err)
	}
	if got, err := cfg.SelectModel(""); err != nil || got.Model != "example" {
		t.Fatalf("single model default: %+v; error: %v", got, err)
	}
	if _, err := cfg.SelectModel("missing"); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, tc := range []struct{ contents, selected string }{
		{"[models.m]\nurl='file:///tmp/file'\nmodel='m'\nprofile='hy-mt2'\n", "m"},
		{"[models.m]\nurl='http://localhost'\nmodel='m'\nprofile='hy-mt2'\nunexpected=1\n", "m"},
		{"[models.m]\nurl='http://localhost'\nmodel=''\nprofile='hy-mt2'\n", "m"},
		{"log_level='verbose'\n[models.m]\nurl='http://localhost'\nmodel='m'\nprofile='hy-mt2'\n", "m"},
		{"model='absent'\n[models.m]\nurl='http://localhost'\nmodel='m'\nprofile='hy-mt2'\n", ""},
	} {
		path := filepath.Join(t.TempDir(), "models.toml")
		if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err == nil {
			_, err = cfg.SelectModel(tc.selected)
		}
		if err == nil {
			t.Fatalf("accepted invalid config: %s", tc.contents)
		}
	}
}

func TestDefaultModel(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "models.toml")
	data := "model='two'\n[models.one]\nurl='http://localhost:1235'\nmodel='one'\nprofile='hy-mt2'\n[models.two]\nurl='http://localhost:1236'\nmodel='two'\nprofile='hy-mt2'\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := cfg.SelectModel(""); err != nil || got.Model != "two" {
		t.Fatalf("configured default: %+v; error: %v", got, err)
	}
	cfg.Model = ""
	if _, err := cfg.SelectModel(""); err == nil {
		t.Fatal("multiple models accepted without a default")
	}
}

func TestLogLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.toml")
	if err := os.WriteFile(path, []byte("log_level='debug'\n[models.test]\nurl='http://localhost'\nmodel='test'\nprofile='hy-mt2'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.LogLevel != "debug" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestVerifyOnlySelectedModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := "adapter='jsonl'\ninput='source/input.jsonl'\nmodel='chosen'\n[models.chosen]\nurl='http://localhost:1235'\nmodel='chosen'\nprofile='hy-mt2'\n[models.other]\nurl=''\nmodel=''\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := cfg.SelectModel("chosen")
	if err != nil || VerifyModel(path, "chosen", selected) != nil {
		t.Fatalf("unselected model blocked chosen model: %v", err)
	}
	selected.URL = "http://localhost:1236"
	if err := VerifyModel(path, "chosen", selected); err == nil {
		t.Fatal("selected model change was not detected")
	}
}
