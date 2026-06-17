package assets

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bresilla/bin/src/pkg/config"
	"github.com/bresilla/bin/src/pkg/options"
	bstrings "github.com/bresilla/bin/src/pkg/strings"
	"github.com/bresilla/bin/src/pkg/ui"
	"github.com/caarlos0/log"
	"github.com/h2non/filetype"
	"github.com/h2non/filetype/matchers"
	"github.com/h2non/filetype/types"
	"github.com/krolaw/zipstream"
	"github.com/xi2/xz"
)

var (
	msiType = filetype.AddType("msi", "application/octet-stream")
	ascType = filetype.AddType("asc", "text/plain")
)

// Quiet suppresses the interactive download progress bar. It's set by the TUI,
// which renders its own UI and can't share the terminal with cheggaaa/pb.
var Quiet bool

type Asset struct {
	Name string
	// Some providers (like gitlab) have non-descriptive names for files,
	// so we're using this DisplayName as a helper to produce prettier
	// outputs for bin
	DisplayName string
	URL         string
}

func (g Asset) String() string {
	if g.DisplayName != "" {
		return g.DisplayName
	}
	return g.Name
}

type FilteredAsset struct {
	RepoName     string
	Name         string
	DisplayName  string
	URL          string
	score        int
	ExtraHeaders map[string]string
	// Fingerprint is the version-normalized, sorted set of installable assets
	// this selection was made from, used later to detect layout changes.
	Fingerprint []string
}

type finalFile struct {
	Source             io.Reader
	Name               string
	PackagePath        string
	PackageFingerprint []string
}

type platformResolver interface {
	GetOS() []string
	GetArch() []string
	GetOSSpecificExtensions() []string
}

type Filter struct {
	opts               *FilterOpts
	repoName           string
	name               string
	packagePath        string
	packageFingerprint []string
}

type FilterOpts struct {
	SkipScoring   bool
	SkipPathCheck bool

	// In case of updates, we're sending the previous version package path
	// so in case it's the same one, we can re-use it.
	PackageName string

	// If target file is in a package format (tar, zip,etc) use this
	// variable to filter the resulting outputs. This is very useful
	// so we don't prompt the user to pick the file again on updates.
	// PackageFingerprint is the version-normalized set of installable files
	// the package path was chosen from — when it's unchanged we reuse the same
	// file (with the new version in its name); when it changes we re-prompt.
	PackagePath        string
	PackageFingerprint []string

	// SelectedAsset is the previously chosen asset (version-normalized) and
	// AssetFingerprint the normalized asset set it was chosen from. When the
	// current release matches the fingerprint, SelectReleaseAsset reuses the
	// remembered choice instead of prompting. Recheck forces a fresh prompt.
	SelectedAsset    string
	AssetFingerprint []string
	Recheck          bool

	// NonInteractive makes asset selection fail instead of prompting when it
	// can't decide on its own. Used by the TUI, which owns the terminal.
	NonInteractive bool

	// Auto makes selection pick the best candidate automatically (preferring
	// musl over glibc/gnu) instead of prompting. Used by `bin ensure`.
	Auto bool
}

type runtimeResolver struct{}

func (runtimeResolver) GetOS() []string {
	return config.GetOS()
}

func (runtimeResolver) GetArch() []string {
	return config.GetArch()
}

func (runtimeResolver) GetOSSpecificExtensions() []string {
	return config.GetOSSpecificExtensions()
}

var resolver platformResolver = runtimeResolver{}

func (g FilteredAsset) String() string {
	if g.DisplayName != "" {
		return g.DisplayName
	}
	return g.Name
}

func NewFilter(opts *FilterOpts) *Filter {
	return &Filter{opts: opts}
}

// assetVersionRe matches version-like number groups (e.g. "0.140.0", "86",
// "64") so they can be collapsed when comparing asset names across releases.
var assetVersionRe = regexp.MustCompile(`[0-9]+(\.[0-9]+)*`)

// NormalizeAssetName lowercases an asset name and replaces version-like number
// groups with a placeholder, so the same asset across different releases (which
// only differ by version) compares equal.
func NormalizeAssetName(name string) string {
	return assetVersionRe.ReplaceAllString(strings.ToLower(name), "#")
}

// Fingerprint returns the sorted, version-normalized names of the given assets.
// It's used to detect when a release's set of installable files has changed
// (files added/removed/renamed) versus a pure version bump.
func Fingerprint(as []*Asset) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, NormalizeAssetName(a.Name))
	}
	sort.Strings(out)
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// installableSuffixes are archive/compression formats bin can unpack.
var installableSuffixes = []string{
	".tar.gz", ".tgz",
	".tar.xz", ".txz",
	".tar.bz2", ".tbz2", ".tbz",
	".tar", ".zip", ".gz", ".xz", ".bz2",
}

// isUsableAsset reports whether an asset is something bin can actually install:
// a supported archive, an OS-specific single file (AppImage/exe), or a raw
// extensionless binary. Signatures, checksums, SBOMs, wheels, distro packages
// and similar metadata files are rejected.
func isUsableAsset(name string) bool {
	n := strings.ToLower(name)
	for _, s := range installableSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	for _, ext := range resolver.GetOSSpecificExtensions() {
		if strings.HasSuffix(n, "."+strings.ToLower(ext)) {
			return true
		}
	}
	// No extension, or a trailing ".<digits>" that's really a version number
	// (e.g. "tool-v0.140.0") => treat as a raw binary.
	ext := strings.TrimPrefix(filepath.Ext(n), ".")
	if ext == "" || isAllDigits(ext) {
		return true
	}
	return false
}

// isAllDigits reports whether s is non-empty and composed solely of digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// rankAsset scores a candidate for automatic selection: musl is preferred over
// a static/neutral build, which is preferred over glibc/gnu.
func rankAsset(name string) int {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "musl"):
		return 2
	case strings.Contains(n, "gnu"), strings.Contains(n, "glibc"):
		return 0
	default:
		return 1
	}
}

// autoPick chooses the best match without prompting: highest rank, ties broken
// by name for determinism.
func autoPick(matches []*FilteredAsset) *FilteredAsset {
	best := matches[0]
	for _, m := range matches[1:] {
		switch r, br := rankAsset(m.Name), rankAsset(best.Name); {
		case r > br:
			best = m
		case r == br && m.Name < best.Name:
			best = m
		}
	}
	return best
}

var tarSuffixes = []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tbz", ".tar"}

// archiveStem returns the asset name without its archive suffix, plus whether
// it was a tar-family or a zip archive.
func archiveStem(name string) (stem string, isTar, isZip bool) {
	n := strings.ToLower(name)
	for _, s := range tarSuffixes {
		if strings.HasSuffix(n, s) {
			return n[:len(n)-len(s)], true, false
		}
	}
	if strings.HasSuffix(n, ".zip") {
		return n[:len(n)-len(".zip")], false, true
	}
	return n, false, false
}

// preferArchiveType collapses tar-vs-zip duplicates of the same asset, keeping
// the format preferred for the current OS: tar on Linux/BSD, zip on macOS and
// Windows. Assets without a tar/zip twin are left untouched.
func preferArchiveType(as []*Asset) []*Asset {
	preferTar := false
	for _, os := range resolver.GetOS() {
		switch os {
		case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
			preferTar = true
		}
	}

	type group struct{ tar, zip, other []*Asset }
	groups := map[string]*group{}
	order := []string{}
	for _, a := range as {
		stem, isTar, isZip := archiveStem(a.Name)
		g := groups[stem]
		if g == nil {
			g = &group{}
			groups[stem] = g
			order = append(order, stem)
		}
		switch {
		case isTar:
			g.tar = append(g.tar, a)
		case isZip:
			g.zip = append(g.zip, a)
		default:
			g.other = append(g.other, a)
		}
	}

	out := make([]*Asset, 0, len(as))
	for _, stem := range order {
		g := groups[stem]
		if len(g.tar) > 0 && len(g.zip) > 0 {
			if preferTar {
				out = append(out, g.tar...)
			} else {
				out = append(out, g.zip...)
			}
		} else {
			out = append(out, g.tar...)
			out = append(out, g.zip...)
		}
		out = append(out, g.other...)
	}
	return out
}

func filterUsableAssets(as []*Asset) []*Asset {
	out := make([]*Asset, 0, len(as))
	for _, a := range as {
		if isUsableAsset(a.Name) {
			out = append(out, a)
		} else {
			log.Debugf("Ignoring asset %q (not an installable archive/binary)", a.Name)
		}
	}
	return out
}

// SelectReleaseAsset chooses which release asset to download. It drops files
// bin can't install, then—unless re-checking or the installable asset layout
// changed since last time—reuses the previously selected asset so the user
// isn't prompted on every update. The returned asset carries the current
// Fingerprint so callers can persist it.
func (f *Filter) SelectReleaseAsset(repoName string, as []*Asset) (*FilteredAsset, error) {
	usable := filterUsableAssets(as)
	if len(usable) == 0 {
		// Nothing recognizable; fall back to the full list so the user can
		// still pick something manually rather than hard-failing.
		log.Debugf("No installable assets after filtering; falling back to full list")
		usable = as
	}
	usable = preferArchiveType(usable)

	fp := Fingerprint(usable)

	// --all shows everything (including filtered-out files) and always prompts.
	selectFrom := usable
	if f.opts.SkipScoring {
		selectFrom = as
	}

	if !f.opts.Recheck && !f.opts.SkipScoring && f.opts.SelectedAsset != "" {
		if stringSlicesEqual(fp, f.opts.AssetFingerprint) {
			for _, a := range usable {
				if NormalizeAssetName(a.Name) == f.opts.SelectedAsset {
					log.Debugf("Reusing remembered asset %q (release layout unchanged)", a.Name)
					return &FilteredAsset{RepoName: repoName, Name: a.Name, DisplayName: a.DisplayName, URL: a.URL, Fingerprint: fp}, nil
				}
			}
			log.Debugf("Remembered asset %q no longer present; re-prompting", f.opts.SelectedAsset)
		} else {
			log.Infof("Release assets changed since last update; please re-select")
		}
	}

	gf, err := f.FilterAssets(repoName, selectFrom)
	if err != nil {
		return nil, err
	}
	gf.Fingerprint = fp
	return gf, nil
}

// FilterAssets receives a slice of GL assets and tries to
// select the proper one and ask the user to manually select one
// in case it can't determine it
func (f *Filter) FilterAssets(repoName string, as []*Asset) (*FilteredAsset, error) {
	matches := []*FilteredAsset{}
	if len(as) == 1 {
		a := as[0]
		matches = append(matches, &FilteredAsset{RepoName: repoName, Name: a.Name, URL: a.URL, score: 0})
	} else {
		if !f.opts.SkipScoring {
			scores := map[string]int{}
			scoreKeys := []string{}
			scores[repoName] = 1
			for _, os := range resolver.GetOS() {
				scores[os] = 10
			}
			for _, arch := range resolver.GetArch() {
				scores[arch] = 5
			}
			for _, osSpecificExtension := range resolver.GetOSSpecificExtensions() {
				scores[osSpecificExtension] = 15
			}

			for key := range scores {
				scoreKeys = append(scoreKeys, strings.ToLower(key))
			}

			for _, a := range as {
				highestScoreForAsset := 0
				gf := &FilteredAsset{RepoName: repoName, Name: a.Name, DisplayName: a.DisplayName, URL: a.URL, score: 0}
				candidate := a.Name
				candidateScore := 0
				if bstrings.ContainsAny(strings.ToLower(candidate), scoreKeys) &&
					isSupportedExt(candidate) {
					for toMatch, score := range scores {
						if strings.Contains(strings.ToLower(candidate), strings.ToLower(toMatch)) {
							log.Debugf("Candidate %s contains %s. Adding score %d", candidate, toMatch, score)
							candidateScore += score
						}
					}
					if candidateScore > highestScoreForAsset {
						highestScoreForAsset = candidateScore
						gf.Name = candidate
						gf.score = candidateScore
					}
				}

				if highestScoreForAsset > 0 {
					matches = append(matches, gf)
				}
			}
			highestAssetScore := 0
			for i := range matches {
				if matches[i].score > highestAssetScore {
					highestAssetScore = matches[i].score
				}
			}
			for i := len(matches) - 1; i >= 0; i-- {
				if matches[i].score < highestAssetScore {
					log.Debugf("Removing %v (URL %v) with score %v lower than %v", matches[i].Name, matches[i].URL, matches[i].score, highestAssetScore)
					matches = append(matches[:i], matches[i+1:]...)
				} else {
					log.Debugf("Keeping %v (URL %v) with highest score %v", matches[i].Name, matches[i].URL, matches[i].score)
				}
			}

		} else {
			log.Debugf("--all flag was supplied, skipping scoring")
			for _, a := range as {
				matches = append(matches, &FilteredAsset{RepoName: repoName, Name: a.Name, DisplayName: a.DisplayName, URL: a.URL, score: 0})
			}
		}
	}

	var gf *FilteredAsset
	if len(matches) == 0 {
		return nil, fmt.Errorf("Could not find any compatible files")
	} else if len(matches) > 1 {
		generic := make([]fmt.Stringer, 0)
		for _, f := range matches {
			generic = append(generic, f)
		}

		sort.SliceStable(generic, func(i, j int) bool {
			return generic[i].String() < generic[j].String()
		})

		// Auto mode (e.g. `bin ensure` without --choose): pick the best match
		// without prompting, preferring musl over glibc/gnu.
		if f.opts.Auto {
			return autoPick(matches), nil
		}

		if f.opts.NonInteractive {
			return nil, fmt.Errorf("multiple matching assets and running non-interactively; run `bin update -r %s` to choose", repoName)
		}

		choice, err := options.Select("Multiple matches found, please select one:", generic)
		if err != nil {
			return nil, err
		}
		gf = choice.(*FilteredAsset)
		// TODO make user select the proper file
	} else {
		gf = matches[0]
	}

	return gf, nil
}

// SanitizeName removes irrelevant information from the
// file name in case it exists
func SanitizeName(name, version string) string {
	name = strings.ToLower(name)
	replacements := []string{}

	// TODO maybe instead of doing this put everything in a map (set) and then
	// generate the replacements? IDK.
	firstPass := true
	for _, osName := range resolver.GetOS() {
		for _, archName := range resolver.GetArch() {
			replacements = append(replacements, "_"+osName+archName, "")
			replacements = append(replacements, "-"+osName+archName, "")
			replacements = append(replacements, "."+osName+archName, "")

			if firstPass {
				replacements = append(replacements, "_"+archName, "")
				replacements = append(replacements, "-"+archName, "")
				replacements = append(replacements, "."+archName, "")
			}
		}

		replacements = append(replacements, "_"+osName, "")
		replacements = append(replacements, "-"+osName, "")
		replacements = append(replacements, "."+osName, "")

		firstPass = false

	}

	replacements = append(replacements, "_"+version, "")
	replacements = append(replacements, "_"+strings.TrimPrefix(version, "v"), "")
	replacements = append(replacements, "-"+version, "")
	replacements = append(replacements, "-"+strings.TrimPrefix(version, "v"), "")
	r := strings.NewReplacer(replacements...)
	return r.Replace(name)
}

// ProcessURL processes a FilteredAsset by uncompressing/unarchiving the URL of the asset.
func (f *Filter) ProcessURL(gf *FilteredAsset) (*finalFile, error) {
	f.name = gf.Name
	// We're not closing the body here since the caller is in charge of that
	req, err := http.NewRequest(http.MethodGet, gf.URL, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range gf.ExtraHeaders {
		req.Header.Add(name, value)
	}
	log.Debugf("Checking binary from %s", gf.URL)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode > 299 || res.StatusCode < 200 {
		return nil, fmt.Errorf("%d response when checking binary from %s", res.StatusCode, gf.URL)
	}

	// We're caching the whole file into memory so we can prompt
	// the user which file they want to download

	var reader io.Reader = res.Body
	if !Quiet {
		pr := ui.NewProgressReader(res.Body, res.ContentLength, gf.String())
		defer pr.Finish()
		reader = pr
	}
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, reader)
	if err != nil {
		return nil, err
	}
	return f.processReader(buf)
}

func (f *Filter) processReader(r io.Reader) (*finalFile, error) {
	var buf bytes.Buffer
	tee := io.TeeReader(r, &buf)

	t, err := filetype.MatchReader(tee)
	if err != nil {
		return nil, err
	}

	outputFile := io.MultiReader(&buf, r)

	type processorFunc func(repoName string, r io.Reader) (*finalFile, error)
	var processor processorFunc
	switch t {
	case matchers.TypeGz:
		processor = f.processGz
	case matchers.TypeTar:
		processor = f.processTar
	case matchers.TypeXz:
		processor = f.processXz
	case matchers.TypeBz2:
		processor = f.processBz2
	case matchers.TypeZip:
		processor = f.processZip
	}

	if processor != nil {
		// log.Debugf("Processing %s file %s with %s", repoName, name, runtime.FuncForPC(reflect.ValueOf(processor).Pointer()).Name())
		outFile, err := processor(f.repoName, outputFile)
		if err != nil {
			return nil, err
		}

		outputFile = outFile.Source

		f.name = outFile.Name
		f.packagePath = outFile.PackagePath
		if len(outFile.PackageFingerprint) > 0 {
			f.packageFingerprint = outFile.PackageFingerprint
		}

		// In case of e.g. a .tar.gz, process the uncompressed archive by calling recursively
		return f.processReader(outputFile)
	}

	return &finalFile{Source: outputFile, Name: f.name, PackagePath: f.packagePath, PackageFingerprint: f.packageFingerprint}, err
}

// processGz receives a tar.gz file and returns the
// correct file for bin to download
func (f *Filter) processGz(name string, r io.Reader) (*finalFile, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}

	return &finalFile{Source: gr, Name: gr.Name}, nil
}

func (f *Filter) processTar(name string, r io.Reader) (*finalFile, error) {
	tr := tar.NewReader(r)
	tarFiles := map[string][]byte{}
	if len(f.opts.PackagePath) > 0 {
		log.Debugf("Processing tag with PackagePath %s\n", f.opts.PackagePath)
	}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		} else if header.FileInfo().IsDir() {
			continue
		}

		if header.Typeflag == tar.TypeReg {
			bs, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			tarFiles[header.Name] = bs
		}
	}
	if len(tarFiles) == 0 {
		return nil, fmt.Errorf("no files found in tar archive")
	}

	selectedFile, err := f.pickArchiveFile(name, tarFiles)
	if err != nil {
		return nil, err
	}
	return &finalFile{Source: bytes.NewReader(tarFiles[selectedFile]), Name: filepath.Base(selectedFile), PackagePath: selectedFile, PackageFingerprint: f.packageFingerprint}, nil
}

func (f *Filter) processBz2(name string, r io.Reader) (*finalFile, error) {
	br := bzip2.NewReader(r)

	return &finalFile{Source: br, Name: name}, nil
}

func (f *Filter) processXz(name string, r io.Reader) (*finalFile, error) {
	xr, err := xz.NewReader(r, 0)
	if err != nil {
		return nil, err
	}

	return &finalFile{Source: xr, Name: name}, nil
}

func (f *Filter) processZip(name string, r io.Reader) (*finalFile, error) {
	zr := zipstream.NewReader(r)

	zipFiles := map[string][]byte{}
	if len(f.opts.PackagePath) > 0 {
		log.Debugf("Processing tag with PackagePath %s\n", f.opts.PackagePath)
	}
	for {
		header, err := zr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		} else if header.Mode().IsDir() {
			continue
		}

		bs, err := io.ReadAll(zr)
		if err != nil {
			return nil, err
		}
		zipFiles[header.Name] = bs
	}
	if len(zipFiles) == 0 {
		return nil, fmt.Errorf("no files found in zip archive")
	}

	selectedFile, err := f.pickArchiveFile(name, zipFiles)
	if err != nil {
		return nil, err
	}
	// return base of selected file since archives usually have folders inside
	return &finalFile{Name: filepath.Base(selectedFile), Source: bytes.NewReader(zipFiles[selectedFile]), PackagePath: selectedFile, PackageFingerprint: f.packageFingerprint}, nil
}

// pickArchiveFile decides which file inside an archive to extract, mirroring
// the release-asset logic one level down:
//  1. keep only installable files (binaries or nested archives),
//  2. if the remembered package fingerprint is unchanged (only versions
//     differ), reuse the same file with the new version in its name — no prompt,
//  3. if the set of files changed (e.g. a new musl build appears) re-prompt,
//  4. a single installable file always auto-selects.
//
// It records the current fingerprint on the Filter so it can be persisted.
func (f *Filter) pickArchiveFile(name string, files map[string][]byte) (string, error) {
	usable := installableCandidates(files)
	fp := Fingerprint(usable)
	f.packageFingerprint = fp

	if !f.opts.Recheck && !f.opts.SkipPathCheck && f.opts.PackagePath != "" {
		want := NormalizeAssetName(f.opts.PackagePath)
		if stringSlicesEqual(fp, f.opts.PackageFingerprint) {
			for _, a := range usable {
				if NormalizeAssetName(a.Name) == want {
					log.Debugf("Reusing remembered package %q as %q (layout unchanged)", f.opts.PackagePath, a.Name)
					return a.Name, nil
				}
			}
			log.Debugf("Remembered package %q not found; re-selecting", f.opts.PackagePath)
		} else {
			log.Infof("Archive contents changed since last update; please re-select")
		}
	}

	choice, err := f.FilterAssets(name, usable)
	if err != nil {
		return "", err
	}
	return choice.String(), nil
}

// isBinaryFile reports whether data is an executable binary by actually
// parsing it as one of the platform object formats (ELF, Mach-O incl. fat,
// or PE). This introspects the real headers rather than sniffing magic bytes,
// so docs/scripts/configs are reliably excluded.
func isBinaryFile(data []byte) bool {
	r := bytes.NewReader(data)
	if f, err := elf.NewFile(r); err == nil {
		f.Close()
		return true
	}
	if f, err := macho.NewFile(r); err == nil {
		f.Close()
		return true
	}
	if f, err := macho.NewFatFile(r); err == nil {
		f.Close()
		return true
	}
	if f, err := pe.NewFile(r); err == nil {
		f.Close()
		return true
	}
	return false
}

// isCompressedFile reports whether data is a supported archive/compression
// wrapper (so nested archives inside an archive stay selectable).
func isCompressedFile(data []byte) bool {
	switch t, _ := filetype.Match(data); t {
	case matchers.TypeGz, matchers.TypeTar, matchers.TypeXz, matchers.TypeBz2, matchers.TypeZip:
		return true
	}
	return false
}

// installableCandidates returns only the files bin can install from an archive
// — executable binaries or nested archives. If none qualify, it falls back to
// all files so the user can still pick manually.
func installableCandidates(files map[string][]byte) []*Asset {
	keep := make([]*Asset, 0)
	all := make([]*Asset, 0, len(files))
	for name, data := range files {
		all = append(all, &Asset{Name: name})
		if isBinaryFile(data) || isCompressedFile(data) {
			keep = append(keep, &Asset{Name: name})
		}
	}
	if len(keep) > 0 {
		return keep
	}
	return all
}

// isSupportedExt checks if this provider supports
// dealing with this specific file extension
func isSupportedExt(filename string) bool {
	if ext := strings.TrimPrefix(filepath.Ext(filename), "."); len(ext) > 0 {
		switch filetype.GetType(ext) {
		case msiType, matchers.TypeDeb, matchers.TypeRpm, ascType:
			log.Debugf("Filename %s doesn't have a supported extension", filename)
			return false
		case matchers.TypeGz, types.Unknown, matchers.TypeZip, matchers.TypeXz, matchers.TypeTar, matchers.TypeBz2, matchers.TypeExe:
			break
		default:
			log.Debugf("Filename %s doesn't have a supported extension", filename)
			return false
		}
	}

	return true
}
