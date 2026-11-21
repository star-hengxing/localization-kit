package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommandWorkspaceSelection(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "source", "inner")
	for _, root := range []string{outer, inner} {
		if err := os.MkdirAll(filepath.Join(root, "source"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("invalid["), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(inner)
	for _, tc := range []struct {
		args  []string
		root  string
		flags []string
	}{
		{[]string{"status", "--details"}, inner, []string{"--details"}},
		{[]string{"init", "--adapter", "vtt"}, inner, []string{"--adapter", "vtt"}},
		{[]string{"status", outer}, outer, nil},
		{[]string{"status", "--id", "--project-config", "--project-config=" + filepath.Join(outer, "config.toml")}, outer, []string{"--id", "--project-config"}},
		{[]string{"eval", "compare", "--baseline", "a", "--candidate", "b", "--out", "c"}, "", nil},
	} {
		got, root, done, err := resolveCommand(tc.args)
		if err != nil || done || root != tc.root {
			t.Fatalf("%v: %q %t %v", tc.args, root, done, err)
		}
		if root != "" && len(tc.flags) > 0 && !reflect.DeepEqual(got[2:], tc.flags) {
			t.Fatalf("%v: %v", tc.args, got)
		}
	}
	for _, args := range [][]string{
		{"status", outer, "--project-config", filepath.Join(inner, "config.toml")},
		{"status", "--project-config", "absent/config.toml"},
		{"status", "--project-config", "my-model.toml"},
		{"translate", "--config", "old-file"},
		{"init", "--project-config", "config.toml"},
		{"eval", "compare", "--project-config", "config.toml"},
	} {
		if _, _, _, err := resolveCommand(args); err == nil {
			t.Errorf("unexpected success: %v", args)
		}
	}
}

func TestMissingInnerSourceDoesNotSelectOuterWorkspace(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "source", "inner")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, "config.toml"), []byte("adapter = 'artc'\ninput = 'source'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "config.toml"), []byte("adapter = 'artc'\ninput = 'source'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inner)
	if _, root, _, err := resolveCommand([]string{"status"}); err != nil || root != inner {
		t.Fatalf("expected inner workspace, got %q: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(inner, "source"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, root, _, err := resolveCommand([]string{"status"}); err != nil || root != inner {
		t.Fatalf("expected inner workspace, got %q: %v", root, err)
	}
	if err := os.MkdirAll(filepath.Join(inner, "materials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "materials", "term-statistics.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(inner, "config.toml")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveCommand([]string{"status"}); err == nil {
		t.Fatal("missing inner configuration selected another workspace")
	}
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, root, _, err := resolveCommand([]string{"status"}); err != nil || root != inner {
		t.Fatalf("expected inner workspace, got %q: %v", root, err)
	}
}

func TestSubdirectoryDoesNotSelectParentWorkspace(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "source")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("adapter = 'artc'\ninput = 'source'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(subdir)
	if _, _, _, err := resolveCommand([]string{"status"}); err == nil {
		t.Fatal("selected parent workspace from subdirectory")
	}
	if _, err := os.Stat(filepath.Join(root, "localization-kit.log")); !os.IsNotExist(err) {
		t.Fatalf("parent log was created: %v", err)
	}
}

func TestHelpNeedsNoWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, args := range [][]string{{"--help"}, {"terms", "--help"}, {"status", "missing", "--help"}, {"polish", "run", "--help"}, {"help", "characters", "init"}, {"status", "-help"}, {"status", "--h"}} {
		if _, _, done, err := resolveCommand(args); !done || err != nil {
			t.Fatalf("%v: %t %v", args, done, err)
		}
	}
	if _, _, _, err := resolveCommand([]string{"status", "--help=true"}); err == nil {
		t.Fatal("help with value accepted")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("%v %v", files, err)
	}
}
