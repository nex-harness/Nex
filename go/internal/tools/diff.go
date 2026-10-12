package tools

// DiffHunk mirrors DiffHunk in the contracts (jsdiff structuredPatch hunk).
type DiffHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// CreateStructuredPatch mirrors createStructuredPatch() in core/src/tool/diff.ts,
// which calls jsdiff structuredPatch(filePath, filePath, old, new, undefined,
// undefined, { context: 3 }). Ported from diff@9 libesm (diff/diff.js line
// tokenizer + diff/base.js Myers + patch/create.js hunk builder).
func CreateStructuredPatch(filePath, oldContent, newContent string) []DiffHunk {
	changes := diffLines(oldContent, newContent)
	return diffLinesResultToPatch(changes, 3)
}

// lineChange is one component of the Myers diff: added, removed or common text.
type lineChange struct {
	value   string
	added   bool
	removed bool
	lines   []string
}

// diffLines mirrors jsdiff diffLines with no options: tokenize into
// content+newline tokens, drop empty tokens, run Myers, join.
func diffLines(oldStr, newStr string) []lineChange {
	oldTokens := removeEmpty(tokenizeLines(oldStr))
	newTokens := removeEmpty(tokenizeLines(newStr))
	components := myersDiff(oldTokens, newTokens)
	return buildValues(components, newTokens, oldTokens)
}

// tokenizeLines mirrors tokenize() in diff/line.js: split into tokens where each
// token is a line including its trailing newline (the final empty token if the
// string ends with a newline is dropped).
func tokenizeLines(value string) []string {
	linesAndNewlines := splitKeepNewlines(value)
	if len(linesAndNewlines) > 0 && linesAndNewlines[len(linesAndNewlines)-1] == "" {
		linesAndNewlines = linesAndNewlines[:len(linesAndNewlines)-1]
	}
	retLines := make([]string, 0, len(linesAndNewlines))
	for i := 0; i < len(linesAndNewlines); i++ {
		line := linesAndNewlines[i]
		if i%2 == 1 {
			retLines[len(retLines)-1] += line
		} else {
			retLines = append(retLines, line)
		}
	}
	return retLines
}

// splitKeepNewlines mirrors `value.split(/(\n|\r\n)/)`: split on LF or CRLF but
// keep the separators as elements.
func splitKeepNewlines(value string) []string {
	var parts []string
	start := 0
	i := 0
	for i < len(value) {
		if value[i] == '\n' {
			parts = append(parts, value[start:i], "\n")
			i++
			start = i
			continue
		}
		if value[i] == '\r' && i+1 < len(value) && value[i+1] == '\n' {
			parts = append(parts, value[start:i], "\r\n")
			i += 2
			start = i
			continue
		}
		i++
	}
	parts = append(parts, value[start:])
	return parts
}

func removeEmpty(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// component mirrors jsdiff's internal linked-list component before buildValues
// joins them; keep it as (count, added, removed) with a link to the previous node.
type component struct {
	count    int
	added    bool
	removed  bool
	previous *component
}

// myersDiff returns the raw components of the Myers diff in order. It follows
// jsdiff diff/base.js `diffWithOptionsObj` exactly, using a map for bestPath
// (JS indexes it by signed diagonal, which a Go slice cannot).
func myersDiff(oldTokens, newTokens []string) []component {
	newLen := len(newTokens)
	oldLen := len(oldTokens)
	bestPath := map[int]*pathItem{0: {oldPos: -1, set: true}}

	newPos := extractCommon(bestPath[0], newTokens, oldTokens, 0)
	if bestPath[0].oldPos+1 >= oldLen && newPos+1 >= newLen {
		return toComponents(*bestPath[0])
	}

	minDiagonal := negInf
	maxDiagonal := posInf
	editLength := 1
	maxEditLength := newLen + oldLen

	for editLength <= maxEditLength {
		for diagonal := maxInt(minDiagonal, -editLength); diagonal <= minInt(maxDiagonal, editLength); diagonal += 2 {
			removePath := bestPath[diagonal-1]
			addPath := bestPath[diagonal+1]
			if removePath != nil {
				delete(bestPath, diagonal-1)
			}
			var canAdd bool
			if addPath != nil {
				addPathNewPos := addPath.oldPos - diagonal
				canAdd = addPathNewPos >= 0 && addPathNewPos < newLen
			}
			canRemove := removePath != nil && removePath.oldPos+1 < oldLen
			if !canAdd && !canRemove {
				delete(bestPath, diagonal)
				continue
			}

			var basePath pathItem
			if removePath == nil || (canAdd && removePath.oldPos < addPath.oldPos) {
				basePath = addToPath(addPath, true, false, 0)
			} else {
				basePath = addToPath(removePath, false, true, 1)
			}
			newPos = extractCommon(&basePath, newTokens, oldTokens, diagonal)
			if basePath.oldPos+1 >= oldLen && newPos+1 >= newLen {
				return toComponents(basePath)
			}
			bestPath[diagonal] = &basePath
			if basePath.oldPos+1 >= oldLen {
				maxDiagonal = minInt(maxDiagonal, diagonal-1)
			}
			if newPos+1 >= newLen {
				minDiagonal = maxInt(minDiagonal, diagonal+1)
			}
		}
		editLength++
	}
	return nil
}

const (
	negInf = -1 << 62
	posInf = 1 << 62
)

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type pathItem struct {
	oldPos int
	last   *component
	set    bool
}

func toComponents(item pathItem) []component {
	var reversed []component
	last := item.last
	for last != nil {
		reversed = append(reversed, *last)
		last = last.previous
	}
	out := make([]component, len(reversed))
	for i := range reversed {
		out[i] = reversed[len(reversed)-1-i]
	}
	return out
}

func extractCommon(basePath *pathItem, newTokens, oldTokens []string, diagonal int) int {
	newLen := len(newTokens)
	oldLen := len(oldTokens)
	oldPos := basePath.oldPos
	newPos := oldPos - diagonal
	commonCount := 0
	for newPos+1 < newLen && oldPos+1 < oldLen && oldTokens[oldPos+1] == newTokens[newPos+1] {
		newPos++
		oldPos++
		commonCount++
	}
	if commonCount > 0 {
		basePath.last = &component{count: commonCount, previous: basePath.last}
	}
	basePath.oldPos = oldPos
	basePath.set = true
	return newPos
}

func addToPath(path *pathItem, added, removed bool, oldPosInc int) pathItem {
	last := path.last
	if last != nil && last.added == added && last.removed == removed {
		return pathItem{
			oldPos: path.oldPos + oldPosInc,
			last:   &component{count: last.count + 1, added: added, removed: removed, previous: last.previous},
			set:    true,
		}
	}
	return pathItem{
		oldPos: path.oldPos + oldPosInc,
		last:   &component{count: 1, added: added, removed: removed, previous: last},
		set:    true,
	}
}

func buildValues(components []component, newTokens, oldTokens []string) []lineChange {
	out := make([]lineChange, 0, len(components))
	newPos, oldPos := 0, 0
	for _, c := range components {
		if !c.removed {
			value := joinTokens(newTokens[newPos : newPos+c.count])
			newPos += c.count
			if !c.added {
				oldPos += c.count
			}
			out = append(out, lineChange{value: value, added: c.added})
		} else {
			value := joinTokens(oldTokens[oldPos : oldPos+c.count])
			oldPos += c.count
			out = append(out, lineChange{value: value, removed: true})
		}
	}
	return out
}

func joinTokens(tokens []string) string {
	total := 0
	for _, t := range tokens {
		total += len(t)
	}
	buf := make([]byte, 0, total)
	for _, t := range tokens {
		buf = append(buf, t...)
	}
	return string(buf)
}

// diffLinesResultToPatch mirrors diffLinesResultToPatch with context lines.
func diffLinesResultToPatch(changes []lineChange, context int) []DiffHunk {
	changes = append(changes, lineChange{value: "", lines: []string{}})
	hunks := []DiffHunk{}
	oldRangeStart, newRangeStart := 0, 0
	var curRange []string
	oldLine, newLine := 1, 1

	for i := 0; i < len(changes); i++ {
		current := &changes[i]
		lines := current.lines
		if lines == nil {
			lines = splitLines(current.value)
			current.lines = lines
		}
		if current.added || current.removed {
			if oldRangeStart == 0 {
				prev := (*lineChange)(nil)
				if i > 0 {
					prev = &changes[i-1]
				}
				oldRangeStart = oldLine
				newRangeStart = newLine
				if prev != nil {
					if context > 0 {
						curRange = contextLines(lastN(prev.lines, context))
					} else {
						curRange = nil
					}
					oldRangeStart -= len(curRange)
					newRangeStart -= len(curRange)
				}
			}
			for _, line := range lines {
				prefix := "-"
				if current.added {
					prefix = "+"
				}
				curRange = append(curRange, prefix+line)
			}
			if current.added {
				newLine += len(lines)
			} else {
				oldLine += len(lines)
			}
		} else {
			if oldRangeStart != 0 {
				if len(lines) <= context*2 && i < len(changes)-2 {
					for _, line := range contextLines(lines) {
						curRange = append(curRange, line)
					}
				} else {
					contextSize := len(lines)
					if context < contextSize {
						contextSize = context
					}
					for _, line := range contextLines(lines[:contextSize]) {
						curRange = append(curRange, line)
					}
					hunks = append(hunks, DiffHunk{
						OldStart: oldRangeStart,
						OldLines: oldLine - oldRangeStart + contextSize,
						NewStart: newRangeStart,
						NewLines: newLine - newRangeStart + contextSize,
						Lines:    curRange,
					})
					oldRangeStart = 0
					newRangeStart = 0
					curRange = nil
				}
			}
			oldLine += len(lines)
			newLine += len(lines)
		}
	}

	for h := range hunks {
		lines := hunks[h].Lines
		for i := 0; i < len(lines); i++ {
			if len(lines[i]) > 0 && lines[i][len(lines[i])-1] == '\n' {
				lines[i] = lines[i][:len(lines[i])-1]
			} else {
				lines = append(lines[:i+1], append([]string{"\\ No newline at end of file"}, lines[i+1:]...)...)
				i++
			}
		}
		hunks[h].Lines = lines
	}
	return hunks
}

func contextLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = " " + l
	}
	return out
}

func lastN(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
