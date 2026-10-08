package main

// This file is a tiny model of what a terminal does with the colour codes in a
// line of output. It exists for one test: a cell is only painted if a background
// colour was set for it and has not been reset since, and the only reliable way
// to know that is to walk the line the way the terminal walks it.

import "strings"

// sgrState is the colour state of the terminal part way through a line.
type sgrState struct {
	// fg and bg are the codes currently in force, or "" for the default.
	fg, bg string
}

// apply reads one escape sequence's worth of codes and returns the new state.
//
// Every number in the sequence is a separate instruction. 38 and 48 introduce a
// colour, and are followed by arguments that are not instructions themselves:
// "5;n" is two numbers and "2;r;g;b" is five. A sequence that only sets a
// foreground says nothing about the background, which is why a coloured run in
// the middle of a line does not disturb the background underneath it.
func (s sgrState) apply(codes string) sgrState {
	if codes == "" {
		// a bare reset puts everything back to the default
		return sgrState{}
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

		case "39": // default foreground
			s.fg = ""
			i++
		case "49": // default background
			s.bg = ""
			i++
		default:
			// bold and the rest: nothing this cares about
			i++
		}
	}

	return s
}

// hasBackground is whether a cell written right now would have a colour of its
// own. A cell with no background is left showing whatever the terminal's own
// background is, which on a terminal set to be transparent is the desktop.
func (s sgrState) hasBackground() bool { return s.bg != "" }
