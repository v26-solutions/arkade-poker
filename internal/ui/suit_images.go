package ui

// suitImageCells describes a 3×3 Kitty graphics virtual placement. The image
// assets are installed by web/suit-images.ts before the browser UI starts.
// The high byte identifies the suit; the low 24 bits come from the foreground
// color. Matching normal and dim image IDs let dialog() dim these cells using
// the same foreground transformation as the rest of the table.
func suitImageCells(suit byte) [3]string {
	diacritics := [...]rune{'\u0305', '\u030d', '\u030e', '\u0310', '\u0312'}
	var rows [3]string
	for y := range rows {
		for x := 0; x < 3; x++ {
			rows[y] += string([]rune{'\U0010eeee', diacritics[y], diacritics[x], diacritics[suit+1]})
		}
	}
	return rows
}
