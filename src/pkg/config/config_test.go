package config

import (
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
