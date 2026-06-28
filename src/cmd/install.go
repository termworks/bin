package cmd

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bresilla/bin/src/pkg/assets"
	"github.com/bresilla/bin/src/pkg/config"
	"github.com/bresilla/bin/src/pkg/providers"
	"github.com/bresilla/bin/src/pkg/ui"
	"github.com/caarlos0/log"
	"github.com/spf13/cobra"
)

// defaultBinName guesses a binary name from an install URL — the repository
// name (e.g. "github.com/pythops/impala" -> "impala").
func defaultBinName(raw string) string {
	s := raw
	for _, p := range []string{"https://", "http://", "docker://", "goinstall://"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	parts := strings.Split(s, "/")
	name := parts[len(parts)-1]
	if name == "" {
		return "bin"
	}
	return name
}

type installCmd struct {
	cmd  *cobra.Command
	opts installOpts
}

type installOpts struct {
	force    bool
	provider string
	all      bool
	noPatch  bool
}

func newInstallCmd() *installCmd {
	root := &installCmd{}
	// nolint: dupl
	cmd := &cobra.Command{
		Use:           "install <url> [name | path]",
		Aliases:       []string{"i", "add"},
		Short:         "Installs the specified binary from a url",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u := args[0]
			packageName := defaultBinName(u)
			defaultPath := config.Get().DefaultPath

			var resolvedPath string
			if len(args) > 1 {
				resolvedPath = args[1]
				if !strings.Contains(resolvedPath, "/") {
					resolvedPath = filepath.Join(defaultPath, resolvedPath)
				}

			} else {
				// Ask what to name the binary, defaulting to the repo name
				// (so "github.com/pythops/impala" installs as "impala", not the
				// mangled asset name).
				name, err := ui.AskString("Install as:", packageName)
				if err != nil {
					return err
				}
				resolvedPath = filepath.Join(defaultPath, name)
			}

			// TODO check if binary already exists in config
			// and triger the update process if that's the case

			p, err := providers.New(u, root.opts.provider)
			if err != nil {
				return err
			}
			log.Debugf("Using provider '%s' for '%s'", p.GetID(), u)

			pResult, err := p.Fetch(&providers.FetchOpts{All: root.opts.all, PackageName: packageName, CollectLibs: !root.opts.noPatch})
			if err != nil {
				return err
			}

			resolvedPath, err = checkFinalPath(resolvedPath, assets.SanitizeName(pResult.Name, pResult.Version))
			if err != nil {
				return err
			}

			hash, err := saveToDisk(pResult, resolvedPath, root.opts.force)
			if err != nil {
				return fmt.Errorf("error installing binary: %w", err)
			}

			// Make the binary runnable on this host: rewrite a missing
			// interpreter and install any bundled libs it needs. No-ops
			// when nothing is broken. If it changed, persist the patched hash so
			// a later ensure doesn't see a mismatch, and record the intent.
			var patched bool
			hash, patched = applyHostPatches(resolvedPath, pResult.Libs, !root.opts.noPatch, hash)

			// Store an absolute path. If the path is already absolute once
			// environment variables are expanded (e.g. "$HOME/.local/bin/foo"),
			// keep that portable form as-is — running filepath.Abs on it would
			// treat the unexpanded "$HOME/…" as relative and wrongly prepend the
			// current directory.
			absPath := resolvedPath
			if !filepath.IsAbs(os.ExpandEnv(resolvedPath)) {
				absPath, err = filepath.Abs(resolvedPath)
				if err != nil {
					return fmt.Errorf("error converting to absolute path: %w", err)
				}
			}

			err = config.UpsertBinary(&config.Binary{
				RemoteName:         pResult.Name,
				Path:               absPath,
				Version:            pResult.Version,
				Hash:               fmt.Sprintf("%x", hash),
				URL:                u,
				Provider:           p.GetID(),
				Description:        fetchDescription(&config.Binary{URL: u, Provider: p.GetID()}),
				PackagePath:        pResult.PackagePath,
				StateURL:           u,
				SelectedAsset:      pResult.SelectedAsset,
				AssetFingerprint:   pResult.AssetFingerprint,
				PackageFingerprint: pResult.PackageFingerprint,
				Tags:               wantedTags(),
				Patch:              patched,
			})

			if err != nil {
				return err
			}

			stepDone("installed", os.ExpandEnv(resolvedPath), pResult.Version)

			return nil
		},
	}

	root.cmd = cmd
	root.cmd.Flags().BoolVarP(&root.opts.force, "force", "f", false, "Force the installation even if the file already exists")
	root.cmd.Flags().BoolVarP(&root.opts.all, "all", "a", false, "Show all possible download options (skip scoring & filtering)")
	root.cmd.Flags().StringVarP(&root.opts.provider, "provider", "p", "", "Forces to use a specific provider")
	root.cmd.Flags().BoolVar(&root.opts.noPatch, "no-patch", false, "Don't auto-fix the ELF interpreter / bundled libs for this host")
	return root
}

// checkFinalPath checks if path exists and if it's a dir or not
// and returns the correct final file path. It also
// checks if the path already exists and prompts
// the user to override
func checkFinalPath(path, fileName string) (string, error) {
	fi, err := os.Stat(os.ExpandEnv(path))

	// TODO implement file existence and override logic
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	if fi != nil && fi.IsDir() {
		return filepath.Join(path, fileName), nil
	}

	return path, nil
}

// saveToDisk saves the specified binary to the desired path
// and makes it executable. It also checks if any other binary
// has the same hash and exists if so.

// TODO check if other binary has the same hash and warn about it.
// TODO if the file is zipped, tared, whatever then extract it
func saveToDisk(f *providers.File, path string, overwrite bool) ([]byte, error) {
	epath := os.ExpandEnv((path))

	extraFlags := os.O_EXCL

	if overwrite {
		extraFlags = 0
		err := os.Remove(epath)
		log.Debugf("Overwrite flag set, removing file %s\n", epath)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	file, err := os.OpenFile(epath, os.O_RDWR|os.O_CREATE|extraFlags, 0o766)
	if err != nil {
		return nil, err
	}

	defer file.Close()

	h := sha256.New()

	tr := io.TeeReader(f.Data, h)

	log.Debugf("Copying for %s@%s into %s", f.Name, f.Version, epath)
	_, err = io.Copy(file, tr)
	if err != nil {
		return nil, err
	}

	file.Close()
	warnMissingLibs(epath)
	return h.Sum(nil), nil
}
