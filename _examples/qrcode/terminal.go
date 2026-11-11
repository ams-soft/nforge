package main

import "strings"

// quietModules is the quiet zone around the symbol, in modules (the standard
// asks for 4).
const quietModules = 4

// renderTerminal draws a module matrix as text, two matrix rows per line with
// the half-block characters ▀ ▄ █, so one module is one column wide and half a
// line tall (about square in a terminal font). The quiet zone is part of the
// output, on every side.
//
// By default the glyphs are the LIGHT modules: terminals usually paint glyphs
// in the foreground color (light) over a dark background, so the code appears
// as dark modules on a light field, which is what scanners expect. With
// invert the glyphs are the DARK modules, for a light terminal theme. No ANSI
// escape is used.
func renderTerminal(m [][]bool, invert bool) string {
	n := len(m)
	side := n + 2*quietModules
	rows := side
	if rows%2 == 1 {
		rows++ // pair up the last line; the extra row is quiet zone
	}
	dark := func(x, y int) bool {
		x, y = x-quietModules, y-quietModules
		return x >= 0 && y >= 0 && x < n && y < n && m[y][x]
	}
	var b strings.Builder
	for y := 0; y < rows; y += 2 {
		for x := range side {
			top, bottom := dark(x, y), dark(x, y+1)
			if !invert { // glyph where the module is light
				top, bottom = !top, !bottom
			}
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
