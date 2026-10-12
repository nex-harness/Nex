package tools

// splitLines mirrors splitLines(): split on \n, keep the trailing newline on each
// line, and mark the final line without a newline by leaving it bare.
func splitLines(text string) []string {
	hasTrailingNl := len(text) > 0 && text[len(text)-1] == '\n'
	raw := splitOnByte(text, '\n')
	result := make([]string, len(raw))
	for i, line := range raw {
		result[i] = line + "\n"
	}
	if hasTrailingNl {
		result = result[:len(result)-1]
	} else {
		last := result[len(result)-1]
		result[len(result)-1] = last[:len(last)-1]
	}
	return result
}

func splitOnByte(s string, b byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
