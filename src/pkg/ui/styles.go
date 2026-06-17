// Package ui holds shared lipgloss styling for bin's pretty CLI output and TUI.
package ui

import (
	"bufio"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	ltable "github.com/charmbracelet/lipgloss/table"
)

// Palette — terminal color indexes used across the CLI and TUI.
//
// These default to ANSI palette names so pywal-style tools can recolor bin by
// changing the terminal palette. They can be overridden per-user via the config file
// (see EnsureTheme / LoadTheme), e.g. with the 232..255 grayscale ramp.
var (
	ColorPrimary = lipgloss.Color("1")  // accent
	ColorOK      = lipgloss.Color("2")  // green
	ColorWarn    = lipgloss.Color("3")  // yellow
	ColorErr     = lipgloss.Color("9")  // bright red
	ColorTag     = lipgloss.Color("6")  // cyan
	ColorMuted   = lipgloss.Color("8")  // gray
	ColorText    = lipgloss.Color("15") // bright white

	// Row backgrounds for the TUI (alternating shades + selected).
	RowBg         = lipgloss.Color("232") // even rows
	RowBgAlt      = lipgloss.Color("235") // odd rows
	RowBgSelected = lipgloss.Color("237") // selected row (closer to accent)
)

// Reusable styles — rebuilt from the palette by applyStyles().
var (
	TitleStyle  lipgloss.Style
	AccentStyle lipgloss.Style
	MutedStyle  lipgloss.Style
	OKStyle     lipgloss.Style
	WarnStyle   lipgloss.Style
	ErrStyle    lipgloss.Style
	TagStyle    lipgloss.Style
	PinStyle    lipgloss.Style
	BorderStyle lipgloss.Style
)

func init() { applyStyles() }

func applyStyles() {
	TitleStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorText).Background(ColorPrimary).Padding(0, 1)
	AccentStyle = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	MutedStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	OKStyle = lipgloss.NewStyle().Foreground(ColorOK)
	WarnStyle = lipgloss.NewStyle().Foreground(ColorWarn)
	ErrStyle = lipgloss.NewStyle().Foreground(ColorErr)
	TagStyle = lipgloss.NewStyle().Foreground(ColorTag)
	PinStyle = lipgloss.NewStyle().Foreground(ColorWarn)
	BorderStyle = lipgloss.NewStyle().Foreground(ColorMuted)
}

// DefaultThemeConf is written to the config file the first time bin runs, so users have
// a documented file to tweak.
const DefaultThemeConf = `# bin TUI theme — colors are terminal palette indexes (0-255) or hex (#aabbcc).
# Palette names recolor automatically with pywal-style tools. The 232..255
# grayscale ramp is handy for subtle row shading.

# foreground colors
accent = 1     # highlights, selection, title background
text   = 15    # primary text
muted  = 8     # secondary text / separators
ok     = 2     # up to date / present
warn   = 3     # update available / pinned
err    = 9     # missing / errors
tag    = 6     # tag chips & repo

# TUI row backgrounds (alternating + selected)
row_bg          = 232  # even rows
row_bg_alt      = 235  # odd rows
row_bg_selected = 237  # selected row
`

// EnsureTheme writes a default config if missing, then loads it.
func EnsureTheme(path string) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = os.WriteFile(path, []byte(DefaultThemeConf), 0o644)
	}
	_ = LoadTheme(path)
}

// LoadTheme reads key=value color overrides from path and rebuilds the styles.
func LoadTheme(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	set := func(c *lipgloss.Color, v string) {
		if v != "" {
			*c = lipgloss.Color(v)
		}
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if i := strings.IndexByte(val, '#'); i >= 0 { // strip trailing comment
			val = strings.TrimSpace(val[:i])
		}
		switch key {
		case "accent":
			set(&ColorPrimary, val)
		case "text":
			set(&ColorText, val)
		case "muted":
			set(&ColorMuted, val)
		case "ok":
			set(&ColorOK, val)
		case "warn":
			set(&ColorWarn, val)
		case "err":
			set(&ColorErr, val)
		case "tag":
			set(&ColorTag, val)
		case "row_bg":
			set(&RowBg, val)
		case "row_bg_alt":
			set(&RowBgAlt, val)
		case "row_bg_selected":
			set(&RowBgSelected, val)
		}
	}
	applyStyles()
	return sc.Err()
}

// Banner renders a small highlighted title chip.
func Banner(s string) string { return TitleStyle.Render(s) }

// Tags renders a list of tags as cyan chips.
func Tags(tags []string) string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, TagStyle.Render(t))
	}
	return strings.Join(out, " ")
}

// StatusDot renders a colored status indicator.
func StatusDot(ok bool) string {
	if ok {
		return OKStyle.Render("● ok")
	}
	return ErrStyle.Render("● missing")
}

// ListRow is one rendered entry for the binary list table.
type ListRow struct {
	Path    string
	Version string
	Tags    []string
	URL     string
	OK      bool
	Pinned  bool
}

// ListTable renders the binary list as a rounded, colorized table.
func ListTable(rows []ListRow) string {
	t := ltable.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(BorderStyle).
		Headers("BINARY", "VERSION", "TAGS", "STATUS", "URL").
		StyleFunc(func(row, col int) lipgloss.Style {
			st := lipgloss.NewStyle().Padding(0, 1)
			if row == ltable.HeaderRow {
				return st.Bold(true).Foreground(ColorPrimary)
			}
			return st
		})

	for _, r := range rows {
		ver := r.Version
		if r.Pinned {
			ver = PinStyle.Render("★ ") + ver
		}
		t.Row(
			r.Path,
			ver,
			Tags(r.Tags),
			StatusDot(r.OK),
			MutedStyle.Render(r.URL),
		)
	}
	return t.String()
}
