package providers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

var ErrInvalidProvider = errors.New("invalid provider")

type File struct {
	Data        io.Reader
	Name        string
	Version     string
	Length      int64
	PackagePath string
	// SelectedAsset is the version-normalized name of the chosen release asset
	// and AssetFingerprint the normalized set of installable assets seen, so
	// the caller can persist them and avoid re-prompting on future updates.
	SelectedAsset    string
	AssetFingerprint []string
	// PackageFingerprint is the normalized set of installable files seen inside
	// the archive, persisted so the inner-file choice can be reused too.
	PackageFingerprint []string
}

func (f *File) Hash() ([]byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, f.Data); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

type FetchOpts struct {
	All            bool
	PackageName    string
	PackagePath    string
	SkipPatchCheck bool
	Version        string
	// SelectedAsset / AssetFingerprint carry the previously remembered choice
	// so providers can reuse it. Recheck forces a fresh prompt.
	SelectedAsset    string
	AssetFingerprint []string
	Recheck          bool
	// PackageFingerprint carries the remembered inner-archive file set.
	PackageFingerprint []string
	// NonInteractive makes asset selection fail instead of prompting.
	NonInteractive bool
}

type Provider interface {
	// Fetch returns the file metadata to retrieve a specific binary given
	// for a provider
	Fetch(*FetchOpts) (*File, error)
	// GetLatestVersion returns the version and the URL of the
	// latest version for this binary
	GetLatestVersion() (string, string, error)

	// GetID returns the unique identiifer of this provider
	GetID() string
}

// Describer is an optional capability for providers that can return the
// upstream repository's one-line description.
type Describer interface {
	GetDescription() (string, error)
}

var (
	httpUrlPrefix      = regexp.MustCompile("^https?://")
	dockerUrlPrefix    = regexp.MustCompile("^docker://")
	goinstallUrlPrefix = regexp.MustCompile("^goinstall://")
)

func New(u, provider string) (Provider, error) {
	if dockerUrlPrefix.MatchString(u) {
		return newDocker(u)
	}
	if goinstallUrlPrefix.MatchString(u) || provider == "goinstall" {
		return newGoInstall(u)
	}
	if !httpUrlPrefix.MatchString(u) {
		u = fmt.Sprintf("https://%s", u)
	}

	purl, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	if strings.Contains(purl.Host, "github") || provider == "github" {
		return newGitHub(purl)
	}

	if strings.Contains(purl.Host, "gitlab") || provider == "gitlab" {
		return newGitLab(purl)
	}

	if strings.Contains(purl.Host, "codeberg") || provider == "codeberg" {
		return newCodeberg(purl)
	}

	if strings.Contains(purl.Host, "releases.hashicorp.com") || provider == "hashicorp" {
		return newHashiCorp(purl)
	}

	return nil, fmt.Errorf("Can't find provider for url %s", u)
}
