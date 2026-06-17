package cmd

import (
	"fmt"

	"github.com/bresilla/bin/src/pkg/ui"
)

// sep prints a full-width separator line.
func sep() { fmt.Println(ui.Rule()) }

// stepHeader prints a styled "▸ name   detail" header for a per-binary action.
func stepHeader(name, detail string) {
	fmt.Printf("%s %s  %s\n",
		ui.AccentStyle.Render("▸"),
		ui.AccentStyle.Render(name),
		ui.MutedStyle.Render(detail),
	)
}

// stepDone prints a styled success line for a per-binary action.
func stepDone(verb, name, version string) {
	fmt.Printf("  %s %s %s %s\n",
		ui.OKStyle.Render("✓"),
		ui.MutedStyle.Render(verb),
		name,
		ui.AccentStyle.Render(version),
	)
}
