// Package palette defines the build-time colors shared by the native and web UI.
// Use six-digit RGB values: browser suit image IDs encode these exact colors.
package palette

const (
	Accent     = "#7cff00"
	Muted      = "#95b77c"
	Border     = "#527339"
	Highlight  = "#111e07"
	Background = "#000000"
	Surface    = "#111111"
	// The pinned Ghostty renderer treats pure black as default foreground.
	// Near-black keeps labels legible on the accent background.
	Ink = "#010101"
)

// Colors supplies the browser bundle and its CSS from the same palette.
func Colors() map[string]string {
	return map[string]string{
		"accent": Accent, "muted": Muted, "border": Border,
		"highlight": Highlight, "background": Background,
		"surface": Surface, "ink": Ink,
	}
}
