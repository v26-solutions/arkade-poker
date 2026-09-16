// Package palette defines the build-time colors shared by the native and web UI.
// Use six-digit RGB values: browser suit image IDs encode these exact colors.
package palette

const (
	Accent     = "#f2f2f2"
	Muted      = "#999999"
	Border     = "#555555"
	Highlight  = "#1a1a1a"
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
