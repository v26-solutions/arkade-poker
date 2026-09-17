// Package palette defines the build-time colors shared by the native and web UI.
// Use six-digit RGB values: browser suit image IDs encode these exact colors.
package palette

const (
	Accent     = "#f2f2f2"
	Muted      = "#999999"
	Border     = "#555555"
	Highlight  = "#ffffff"
	Background = "#094c00"
	Surface    = "#111111"
	// Ink is the foreground on light surfaces: selected button labels,
	// result badges, and highlighted card ranks and suits.
	Ink = "#094c00"
)

// Colors supplies the browser bundle and its CSS from the same palette.
func Colors() map[string]string {
	return map[string]string{
		"accent": Accent, "muted": Muted, "border": Border,
		"highlight": Highlight, "background": Background,
		"surface": Surface, "ink": Ink,
	}
}
