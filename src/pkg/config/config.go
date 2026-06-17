package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/caarlos0/log"
)

var cfg config

type config struct {
	// DefaultPath might not be expanded so it's important that
	// the caller expands this variable with os.ExpandEnv(string)
	// if necessary
	DefaultPath string             `json:"default_path"`
	Bins        map[string]*Binary `json:"bins"`
}

type Binary struct {
	Path       string `json:"path"`
	RemoteName string `json:"remote_name"`
	Version    string `json:"version"`
	Hash       string `json:"hash"`
	URL        string `json:"url"`
	Provider   string `json:"provider"`
	// Description is the upstream repository's one-line description, persisted
	// in the manifest so the TUI can show it without hitting the network.
	Description string `json:"description,omitempty"`
	// if file is installed from a package format (zip, tar, etc) store
	// the package path in config so we don't ask the user to select
	// the path again when upgrading
	PackagePath string `json:"package_path"`
	Pinned      bool   `json:"pinned"`
	// Tags group binaries into tiers (e.g. "default", "essential"). A binary
	// with no tags is treated as belonging to "default". Persisted in the
	// manifest since they're portable, not per-machine state.
	Tags []string `json:"tags,omitempty"`
	// StateURL holds a release- or version-specific URL, persisted only in state
	StateURL string `json:"-"`
	// SelectedAsset is the version-normalized name of the release asset the
	// user picked. AssetFingerprint is the version-normalized, sorted set of
	// installable assets seen at selection time. Both are persisted only in
	// state and used to skip re-prompting unless the release layout changes.
	SelectedAsset    string   `json:"-"`
	AssetFingerprint []string `json:"-"`
	// PackageFingerprint is the normalized set of installable files seen inside
	// the archive at selection time (state-only), used to reuse or re-prompt the
	// inner-file choice across updates.
	PackageFingerprint []string `json:"-"`
}

// stateEntry contains per-machine mutable data
// persisted separately from the manifest
type stateEntry struct {
	Version            string   `json:"version"`
	RemoteName         string   `json:"remote_name,omitempty"`
	Hash               string   `json:"hash"`
	PackagePath        string   `json:"package_path"`
	Pinned             bool     `json:"pinned"`
	URL                string   `json:"url"`
	SelectedAsset      string   `json:"selected_asset,omitempty"`
	AssetFingerprint   []string `json:"asset_fingerprint,omitempty"`
	PackageFingerprint []string `json:"package_fingerprint,omitempty"`
}

type state struct {
	Bins map[string]*stateEntry `json:"bins"`
}

func CheckAndLoad() error {
	configPath, err := getConfigPath()
	if err != nil {
		return err
	}

	confDir := filepath.Dir(configPath)
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("Error creating config directory [%v]", err)
	}
	log.Debugf("Config directory is: %s", confDir)

	// Load manifest (may not exist yet)
	mf, err := os.OpenFile(configPath, os.O_RDWR|os.O_CREATE, 0664)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	defer mf.Close()
	if err := json.NewDecoder(mf).Decode(&cfg); err != nil {
		if err == io.EOF {
			cfg.Bins = map[string]*Binary{}
		} else {
			return err
		}
	}
	if cfg.Bins == nil {
		cfg.Bins = map[string]*Binary{}
	}

	// Re-key any entries that aren't keyed by their Path before we overlay
	// state below. The rest of the codebase relies on key==Path.
	keysChanged := normalizeManifestKeys()

	// Detect a legacy manifest that still stores remote_name (now state-only):
	// any RemoteName present right after decoding came from the manifest, so a
	// rewrite is needed to move it into the state file.
	remoteNameInManifest := false
	for _, b := range cfg.Bins {
		if b != nil && b.RemoteName != "" {
			remoteNameInManifest = true
			break
		}
	}

	// Load state and overlay
	sp, err := getStatePath(configPath)
	if err != nil {
		return err
	}
	st := state{Bins: map[string]*stateEntry{}}
	if sf, err := os.Open(sp); err == nil {
		defer sf.Close()
		_ = json.NewDecoder(sf).Decode(&st)
	}

	// Ensure current bins carry "default" before merging siblings, so a binary
	// present in both the main config and a sibling keeps "default" and gains
	// the sibling's tag (rather than silently losing its default membership).
	preTags := normalizeTags()

	// One-shot migration: fold any sibling manifests (e.g. other.json) into the
	// single config, tagging their binaries by filename, then .bak the source.
	mergeChanged := mergeSiblingManifests(configPath, &st)

	for k, sb := range st.Bins {
		if b, ok := cfg.Bins[k]; ok && sb != nil {
			b.Version = sb.Version
			if sb.RemoteName != "" {
				b.RemoteName = sb.RemoteName
			}
			b.Hash = sb.Hash
			b.PackagePath = sb.PackagePath
			b.Pinned = sb.Pinned
			b.StateURL = sb.URL
			b.SelectedAsset = sb.SelectedAsset
			b.AssetFingerprint = sb.AssetFingerprint
			b.PackageFingerprint = sb.PackageFingerprint
		}
	}

	// If DefaultPath not set, prompt user and write both files
	if len(cfg.DefaultPath) == 0 {
		cfg.DefaultPath, err = getDefaultPath()
		if err != nil {
			for {
				log.Info("Could not find a PATH directory automatically, falling back to manual selection")
				reader := bufio.NewReader(os.Stdin)
				var response string
				fmt.Printf("\nPlease specify a download directory: ")
				response, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("Invalid input")
				}
				response = strings.TrimSpace(response)

				if err = checkDirExistsAndWritable(response); err != nil {
					log.Debugf("Could not set download directory [%s]: [%v]", response, err)
					continue
				}

				cfg.DefaultPath = response
				break
			}
		}

		if err := writeAll(); err != nil {
			return err
		}
	}

	// Migration: if manifest contains state but state file is empty, split
	needsMigration := false
	if len(st.Bins) == 0 {
		for _, b := range cfg.Bins {
			if b == nil {
				continue
			}
			if b.Version != "" || b.Hash != "" || b.PackagePath != "" || b.Pinned {
				needsMigration = true
				break
			}
		}
	}
	if needsMigration {
		log.Infof("Splitting config manifest and state into %s and %s", configPath, sp)
		if err := writeAll(); err != nil {
			return err
		}
	}

	// Ensure every binary has at least the "default" tag.
	tagsChanged := normalizeTags()

	// Normalize URLs in manifest to base repository links when possible
	urlsChanged := normalizeManifestURLs()
	providersChanged := normalizeProviders()
	if urlsChanged || providersChanged || keysChanged || mergeChanged || preTags || tagsChanged || remoteNameInManifest {
		if err := writeAll(); err != nil {
			return err
		}
	}

	log.Debugf("Download path set to %s", cfg.DefaultPath)
	return nil
}

// normalizeManifestKeys ensures every binary is keyed by its Path field.
// Older or hand-edited manifests sometimes key entries by the bare binary
// name (e.g. "codex" instead of "$HOME/.local/bin/codex"), which breaks the
// key==Path invariant the rest of the code relies on and leads to nil map
// lookups (e.g. a panic on `bin update <name>`). Returns true if it modified cfg.
func normalizeManifestKeys() bool {
	changed := false
	rekey := map[string]*Binary{}
	for k, b := range cfg.Bins {
		if b == nil {
			delete(cfg.Bins, k)
			changed = true
			continue
		}
		if b.Path != "" && k != b.Path {
			rekey[k] = b
		}
	}
	for k, b := range rekey {
		log.Debugf("Re-keying manifest entry %q to %q", k, b.Path)
		delete(cfg.Bins, k)
		cfg.Bins[b.Path] = b
		changed = true
	}
	return changed
}

// addTag returns tags with t appended if not already present.
func addTag(tags []string, t string) []string {
	for _, x := range tags {
		if x == t {
			return tags
		}
	}
	return append(tags, t)
}

// normalizeTags ensures every binary has at least the "default" tag.
// Returns true if it modified cfg.
func normalizeTags() bool {
	changed := false
	for _, b := range cfg.Bins {
		if b != nil && len(b.Tags) == 0 {
			b.Tags = []string{"default"}
			changed = true
		}
	}
	return changed
}

// mergeSiblingManifests folds any other *.json manifests living next to the
// main config (e.g. a legacy other.json) into the single config, tagging the
// merged binaries by the source filename (other.json -> "other"). Their state
// files are merged too, and the sources are renamed to *.bak so the merge runs
// only once. Returns true if anything was merged.
func mergeSiblingManifests(configPath string, st *state) bool {
	dir := filepath.Dir(configPath)
	mainBase := filepath.Base(configPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	changed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == mainBase ||
			!strings.HasSuffix(name, ".json") ||
			strings.HasSuffix(name, ".state.json") {
			continue
		}
		tag := strings.TrimSuffix(name, ".json")
		sibPath := filepath.Join(dir, name)

		var sib config
		sf, oerr := os.Open(sibPath)
		if oerr != nil {
			continue
		}
		derr := json.NewDecoder(sf).Decode(&sib)
		sf.Close()
		if derr != nil && derr != io.EOF {
			log.Warnf("Skipping sibling config %s: %v", name, derr)
			continue
		}

		merged := 0
		for _, b := range sib.Bins {
			if b == nil || b.Path == "" {
				continue
			}
			if existing, ok := cfg.Bins[b.Path]; ok {
				existing.Tags = addTag(existing.Tags, tag)
			} else {
				b.Tags = addTag(b.Tags, tag)
				cfg.Bins[b.Path] = b
			}
			merged++
		}

		// Merge the sibling's state (versions/hashes) so nothing is lost.
		if ssp, serr := getStatePath(sibPath); serr == nil {
			if ssf, e2 := os.Open(ssp); e2 == nil {
				var sst state
				if json.NewDecoder(ssf).Decode(&sst) == nil {
					for k, se := range sst.Bins {
						if se == nil {
							continue
						}
						if _, ok := st.Bins[k]; !ok {
							st.Bins[k] = se
						}
					}
				}
				ssf.Close()
				_ = os.Rename(ssp, ssp+".bak")
			}
		}

		if err := os.Rename(sibPath, sibPath+".bak"); err != nil {
			log.Warnf("Merged %s but could not rename it: %v", name, err)
		}
		log.Infof("Merged %d binaries from %s as tag %q", merged, name, tag)
		changed = true
	}
	return changed
}

// normalizeProviders backfills an empty Provider from the URL host so older or
// hand-edited manifests don't carry blank providers. Returns true if changed.
func normalizeProviders() bool {
	changed := false
	for _, b := range cfg.Bins {
		if b == nil || b.Provider != "" || b.URL == "" {
			continue
		}
		host := ""
		if u, err := url.Parse(b.URL); err == nil {
			host = u.Host
		}
		switch {
		case strings.Contains(host, "github"):
			b.Provider = "github"
		case strings.Contains(host, "gitlab"):
			b.Provider = "gitlab"
		case strings.Contains(host, "codeberg"):
			b.Provider = "codeberg"
		case strings.Contains(host, "releases.hashicorp.com"):
			b.Provider = "hashicorp"
		default:
			continue
		}
		changed = true
	}
	return changed
}

// normalizeManifestURLs rewrites manifest URLs to stable base links
// (e.g. https://github.com/owner/repo) when they currently point
// at release/tag or download URLs. Returns true if it modified cfg.
func normalizeManifestURLs() bool {
	changed := false
	for _, b := range cfg.Bins {
		if b == nil || b.URL == "" {
			continue
		}
		base := normalizeBaseURL(b.URL, b.Provider)
		if base != "" && base != b.URL {
			// Preserve the original, potentially version-specific URL in state
			if b.StateURL == "" {
				b.StateURL = b.URL
			}
			log.Debugf("Normalizing manifest URL from %s to %s", b.URL, base)
			b.URL = base
			changed = true
		}
	}
	return changed
}

// normalizeBaseURL attempts to derive a stable repository/home URL from
// a potentially versioned or release-specific URL.
func normalizeBaseURL(raw, provider string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	// If provider isn't set in the manifest (older entries), infer it from host
	inferredProvider := provider
	if inferredProvider == "" {
		host := u.Host
		if strings.Contains(host, "github") {
			inferredProvider = "github"
		} else if strings.Contains(host, "codeberg") {
			inferredProvider = "codeberg"
		} else if strings.Contains(host, "gitlab") {
			inferredProvider = "gitlab"
		}
	}

	switch inferredProvider {
	case "github", "codeberg", "gitlab":
		parts := strings.Split(u.Path, "/")
		if len(parts) >= 3 {
			return fmt.Sprintf("%s://%s/%s/%s", u.Scheme, u.Host, parts[1], parts[2])
		}
	}
	return ""
}

func Get() *config {
	return &cfg
}

// ConfigDir returns the directory holding the manifest (and config).
func ConfigDir() string {
	p, err := getConfigPath()
	if err != nil {
		return ""
	}
	return filepath.Dir(p)
}

// UpsertBinary adds or updats an existing
// binary resource in the config
func UpsertBinary(c *Binary) error {
	if c != nil {
		// Preserve existing state-only fields unless the caller overrides them
		if existing, ok := cfg.Bins[c.Path]; ok {
			if c.StateURL == "" {
				c.StateURL = existing.StateURL
			}
			if c.SelectedAsset == "" {
				c.SelectedAsset = existing.SelectedAsset
			}
			if len(c.AssetFingerprint) == 0 {
				c.AssetFingerprint = existing.AssetFingerprint
			}
			if len(c.PackageFingerprint) == 0 {
				c.PackageFingerprint = existing.PackageFingerprint
			}
			if c.RemoteName == "" {
				c.RemoteName = existing.RemoteName
			}
			// Tags live in the manifest; preserve them unless the caller is
			// explicitly setting them (e.g. install or the `tag` command).
			if len(c.Tags) == 0 {
				c.Tags = existing.Tags
			}
			if c.Description == "" {
				c.Description = existing.Description
			}
		}
		if len(c.Tags) == 0 {
			c.Tags = []string{"default"}
		}
		cfg.Bins[c.Path] = c
		if err := writeAll(); err != nil {
			return err
		}
	}
	return nil
}

// RemoveBinaries removes the specified paths
// from bin configuration. It doesn't care about the order
func RemoveBinaries(paths []string) error {
	for _, p := range paths {
		delete(cfg.Bins, p)
	}
	return writeAll()
}

// writeAll writes manifest and state to their respective locations
func writeAll() error {
	configPath, err := getConfigPath()
	if err != nil {
		return err
	}
	statePath, err := getStatePath(configPath)
	if err != nil {
		return err
	}
	if err := writeManifest(configPath); err != nil {
		return err
	}
	if err := writeState(statePath); err != nil {
		return err
	}
	return nil
}

type manifestConfig struct {
	DefaultPath string                     `json:"default_path"`
	Bins        map[string]*manifestBinary `json:"bins"`
}

type manifestBinary struct {
	Path        string   `json:"path"`
	URL         string   `json:"url"`
	Provider    string   `json:"provider"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

func writeManifest(manifestPath string) error {
	dir := filepath.Dir(manifestPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(manifestPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0664)
	if err != nil {
		return err
	}
	defer f.Close()

	// sanitize state fields out of manifest
	out := manifestConfig{DefaultPath: cfg.DefaultPath, Bins: map[string]*manifestBinary{}}
	for k, b := range cfg.Bins {
		if b == nil {
			continue
		}
		out.Bins[k] = &manifestBinary{
			Path:        b.Path,
			URL:         b.URL,
			Provider:    b.Provider,
			Description: b.Description,
			Tags:        b.Tags,
		}
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	return enc.Encode(out)
}

func writeState(statePath string) error {
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(statePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0664)
	if err != nil {
		return err
	}
	defer f.Close()

	st := state{Bins: map[string]*stateEntry{}}
	for k, b := range cfg.Bins {
		if b == nil {
			continue
		}
		st.Bins[k] = &stateEntry{Version: b.Version, RemoteName: b.RemoteName, Hash: b.Hash, PackagePath: b.PackagePath, Pinned: b.Pinned, URL: b.StateURL, SelectedAsset: b.SelectedAsset, AssetFingerprint: b.AssetFingerprint, PackageFingerprint: b.PackageFingerprint}
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "    ")
	return enc.Encode(st)
}

// GetArch is the running program's operating system target:
// one of darwin, freebsd, linux, and so on.
func GetArch() []string {
	res := []string{runtime.GOARCH}
	if runtime.GOARCH == "amd64" {
		res = append(res, "x86_64")
		res = append(res, "x64")
	}
	return res
}

// GetOS is the running program's architecture target:
// one of 386, amd64, arm, s390x, and so on.
func GetOS() []string {
	res := []string{runtime.GOOS}
	if runtime.GOOS == "windows" {
		res = append(res, "win")
	}
	return res
}

// getConfigPath returns the path to the manifest file (list.json) respecting
// the `XDG Base Directory specification` using the following strategy:
//   - to prevent breaking of existing configurations, check if "$HOME/.bin/list.json"
//     exists and return "$HOME/.bin"
//   - if "XDG_CONFIG_HOME" is set, return "$XDG_CONFIG_HOME/bin"
//   - if "$HOME/.config" exists, return "$home/.config/bin"
//   - default to "$HOME/.bin/"
func getConfigPath() (string, error) {
	var c string

	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		if _, err := os.Stat(filepath.Join(home, ".bin", "list.json")); !os.IsNotExist(err) {
			return filepath.Join(path.Join(home, ".bin", "list.json")), nil
		}
	}

	c = os.Getenv("XDG_CONFIG_HOME")
	if c != "" {
		return filepath.Join(c, "bin", "list.json"), nil
	}
	if homeErr != nil {
		return "", homeErr
	}
	c = filepath.Join(home, ".config")
	if _, err := os.Stat(c); !os.IsNotExist(err) {
		return filepath.Join(c, "bin", "list.json"), nil
	}
	return filepath.Join(home, ".bin", "list.json"), nil
}

// getStatePath computes the per-machine state file path derived from manifest path
func getStatePath(manifestPath string) (string, error) {
	base := filepath.Base(manifestPath)
	name := strings.TrimSuffix(base, filepath.Ext(base)) + ".state.json"
	// Prefer XDG_DATA_HOME
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "bin", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "bin", name), nil
	case "windows":
		if ld := os.Getenv("LOCALAPPDATA"); ld != "" {
			return filepath.Join(ld, "bin", name), nil
		}
		if ad := os.Getenv("APPDATA"); ad != "" {
			return filepath.Join(ad, "bin", name), nil
		}
		return filepath.Join(home, ".local", "share", "bin", name), nil
	default:
		return filepath.Join(home, ".local", "share", "bin", name), nil
	}
}

func GetOSSpecificExtensions() []string {
	switch runtime.GOOS {
	case "linux":
		return []string{"AppImage"}
	case "windows":
		return []string{"exe"}
	default:
		return nil
	}
}
