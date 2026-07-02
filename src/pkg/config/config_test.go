package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestForgetBinarySelectionClearsOnlySavedChoices(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))

	path := "$HOME/.local/bin/atuin"
	cfg = config{
		DefaultPath: "$HOME/.local/bin",
		Bins: map[string]*Binary{
			path: {
				Path:               path,
				RemoteName:         "atuin-server",
				Version:            "v1.0.0",
				Hash:               "abc123",
				URL:                "https://github.com/atuinsh/atuin",
				Provider:           "github",
				Description:        "shell history",
				PackagePath:        "atuin-server",
				Pinned:             true,
				Tags:               []string{"default", "shell"},
				Patch:              true,
				StateURL:           "https://github.com/atuinsh/atuin/releases/tag/v1.0.0",
				SelectedAsset:      "atuin-server-aarch64-unknown-linux-musl.tar.gz",
				AssetFingerprint:   []string{"old-asset"},
				PackageFingerprint: []string{"old-package"},
			},
		},
	}

	if err := ForgetBinarySelection(path); err != nil {
		t.Fatalf("ForgetBinarySelection: %v", err)
	}

	b := cfg.Bins[path]
	if b.RemoteName != "" || b.PackagePath != "" || b.SelectedAsset != "" ||
		len(b.AssetFingerprint) != 0 || len(b.PackageFingerprint) != 0 {
		t.Fatalf("selection memory was not cleared: %+v", b)
	}
	if b.Version != "v1.0.0" || b.Hash != "abc123" || b.URL != "https://github.com/atuinsh/atuin" ||
		b.Provider != "github" || b.Description != "shell history" || !b.Pinned || !b.Patch ||
		b.StateURL != "https://github.com/atuinsh/atuin/releases/tag/v1.0.0" ||
		len(b.Tags) != 2 || b.Tags[0] != "default" || b.Tags[1] != "shell" {
		t.Fatalf("non-selection fields changed unexpectedly: %+v", b)
	}
}

func TestPathOverridesForDeclarativeIntegrations(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "cfg", "list.json")
	stateFile := filepath.Join(tmp, "state", "state.json")

	t.Setenv("BIN_CONFIG_FILE", configFile)
	t.Setenv("BIN_STATE_FILE", stateFile)

	gotConfig, err := getConfigPath()
	if err != nil {
		t.Fatalf("getConfigPath: %v", err)
	}
	if gotConfig != configFile {
		t.Fatalf("config path = %q, want %q", gotConfig, configFile)
	}

	gotState, err := getStatePath(configFile)
	if err != nil {
		t.Fatalf("getStatePath: %v", err)
	}
	if gotState != stateFile {
		t.Fatalf("state path = %q, want %q", gotState, stateFile)
	}
}

func TestNonInteractiveDefaultPathFromEnvironment(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BIN_CONFIG_FILE", filepath.Join(tmp, "list.json"))
	t.Setenv("BIN_STATE_FILE", filepath.Join(tmp, "state.json"))
	t.Setenv("BIN_DEFAULT_PATH", filepath.Join(tmp, "bin"))
	t.Setenv("BIN_NONINTERACTIVE", "1")
	cfg = config{}

	if err := CheckAndLoad(); err != nil {
		t.Fatalf("CheckAndLoad: %v", err)
	}
	if cfg.DefaultPath != filepath.Join(tmp, "bin") {
		t.Fatalf("DefaultPath = %q", cfg.DefaultPath)
	}
}

func TestExplicitConfigPathDoesNotMergeSiblingJSON(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "list.json")
	sibling := filepath.Join(tmp, "desired.json")

	t.Setenv("BIN_CONFIG_FILE", configFile)
	t.Setenv("BIN_STATE_FILE", filepath.Join(tmp, "state.json"))
	t.Setenv("BIN_DEFAULT_PATH", filepath.Join(tmp, "bin"))
	t.Setenv("BIN_NONINTERACTIVE", "1")
	cfg = config{}

	if err := os.WriteFile(sibling, []byte(`{"not":"a bin manifest"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckAndLoad(); err != nil {
		t.Fatalf("CheckAndLoad: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling JSON should not be renamed/merged: %v", err)
	}
	if _, err := os.Stat(sibling + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("sibling JSON was unexpectedly renamed to .bak")
	}
}

func TestStateDescriptionOverlaysDeclarativeManifest(t *testing.T) {
	tmp := t.TempDir()
	configFile := filepath.Join(tmp, "list.json")
	stateFile := filepath.Join(tmp, "state.json")
	installDir := filepath.Join(tmp, "bin")
	binPath := filepath.Join(installDir, "mdbook")

	t.Setenv("BIN_CONFIG_FILE", configFile)
	t.Setenv("BIN_STATE_FILE", stateFile)
	t.Setenv("BIN_DEFAULT_PATH", installDir)
	t.Setenv("BIN_NONINTERACTIVE", "1")
	cfg = config{}

	if err := os.WriteFile(configFile, []byte(`{
		"default_path": "`+installDir+`",
		"bins": {
			"`+binPath+`": {
				"path": "`+binPath+`",
				"url": "github.com/rust-lang/mdBook",
				"provider": "github",
				"tags": ["default"],
				"patch": true
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(`{
		"bins": {
			"`+binPath+`": {
				"version": "v0.5.3",
				"hash": "abc123",
				"package_path": "mdbook",
				"url": "github.com/rust-lang/mdBook",
				"description": "Create book from markdown files"
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CheckAndLoad(); err != nil {
		t.Fatalf("CheckAndLoad: %v", err)
	}

	got := cfg.Bins[binPath]
	if got == nil {
		t.Fatal("expected mdbook entry")
	}
	if got.Description != "Create book from markdown files" {
		t.Fatalf("Description = %q", got.Description)
	}
	if got.Version != "v0.5.3" || got.Hash != "abc123" || got.PackagePath != "mdbook" {
		t.Fatalf("state fields were not overlaid: %+v", got)
	}
}
