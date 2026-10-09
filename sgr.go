package main

// A tiny model of what a terminal does with colour codes, so a test can walk a
// line the way the terminal walks it and see which cells are actually painted.

import "strings"

// The colour state part way through a line: the codes in force, or "" for the
// default.
type sgrState struct {
	fg, bg string
}

// Reads one escape sequence's worth of codes and returns the new state.
//
// Every number is a separate instruction. 38 and 48 introduce a colour and are
// followed by arguments that are not instructions: "5;n" is two numbers and
// "2;r;g;b" is five. A sequence setting only a foreground says nothing about the
// background, which is why a coloured run mid-line leaves the background under it
// alone.
func (s sgrState) apply(codes string) sgrState {
	if codes == "" {
		return sgrState{} // a bare reset puts everything back to the default
	}

	parts := strings.Split(codes, ";")
	for i := 0; i < len(parts); {
		switch parts[i] {
		case "", "0":
			s = sgrState{}
			i++

		case "38", "48", "58":
			if i+1 >= len(parts) {
				i++
				continue
			}
			end := i + 2
			if parts[i+1] == "2" {
				end = i + 5
			}
			if end > len(parts) {
				end = len(parts)
			}
			value := strings.Join(parts[i:end], ";")
			switch parts[i] {
			case "38":
				s.fg = value
			case "48":
				s.bg = value
			}
			i = end

		case "39":
			s.fg = ""
			i++
		case "49":
			s.bg = ""
			i++
		default:
			i++ // bold and the rest: nothing this cares about
		}
	}

	return s
}

// Whether a cell written right now would have a colour of its own. A cell with
// no background shows the terminal's own, which on a transparent terminal is
// the desktop.
func (s sgrState) hasBackground() bool { return s.bg != "" }
