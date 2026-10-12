package tools

import (
	"regexp"
	"strings"
)

// EditMatchStrategy mirrors EditMatchStrategy in edit-matchers.ts.
type EditMatchStrategy string

const (
	StrategyExact                    EditMatchStrategy = "exact"
	StrategyQuoteNormalized          EditMatchStrategy = "quote_normalized"
	StrategyLineNumberPrefixStripped EditMatchStrategy = "line_number_prefix_stripped"
	StrategyEscapeNormalized         EditMatchStrategy = "escape_normalized"
	StrategyUnicodeEscapeNormalized  EditMatchStrategy = "unicode_escape_normalized"
	StrategyLineTrimmed              EditMatchStrategy = "line_trimmed"
	StrategyIndentationFlexible      EditMatchStrategy = "indentation_flexible"
	StrategyBlockAnchor              EditMatchStrategy = "block_anchor"
)

var broadMatchers = map[EditMatchStrategy]bool{
	StrategyLineTrimmed:         true,
	StrategyIndentationFlexible: true,
	StrategyBlockAnchor:         true,
}

const blockAnchorMinSimilarity = 0.8

const (
	leftSingleCurly  = "\u2018"
	rightSingleCurly = "\u2019"
	leftDoubleCurly  = "\u201c"
	rightDoubleCurly = "\u201d"
)

// EditMatchResult mirrors EditMatchResult.
type EditMatchResult struct {
	Status         string // matched | ambiguous | not_found
	ActualString   string
	Strategy       EditMatchStrategy
	CandidateCount int
}

type candidate struct {
	value string
	index int
}

// FindEditMatch mirrors findEditMatch().
func FindEditMatch(content, search string, replaceAll bool) EditMatchResult {
	exact := collectExactCandidates(content, search)
	if len(exact) > 0 {
		return toMatchResult(StrategyExact, exact)
	}
	strategies := []EditMatchStrategy{
		StrategyQuoteNormalized,
		StrategyLineNumberPrefixStripped,
		StrategyEscapeNormalized,
		StrategyUnicodeEscapeNormalized,
		StrategyLineTrimmed,
		StrategyIndentationFlexible,
		StrategyBlockAnchor,
	}
	for _, strategy := range strategies {
		if replaceAll && broadMatchers[strategy] {
			continue
		}
		candidates := collectCandidates(strategy, content, search)
		if len(candidates) == 0 {
			continue
		}
		return toMatchResult(strategy, candidates)
	}
	return EditMatchResult{Status: "not_found"}
}

func toMatchResult(strategy EditMatchStrategy, candidates []candidate) EditMatchResult {
	unique := map[string]bool{}
	order := []string{}
	for _, c := range candidates {
		if !unique[c.value] {
			unique[c.value] = true
			order = append(order, c.value)
		}
	}
	if len(order) != 1 {
		return EditMatchResult{Status: "ambiguous", Strategy: strategy, CandidateCount: len(candidates)}
	}
	return EditMatchResult{
		Status:         "matched",
		ActualString:   order[0],
		Strategy:       strategy,
		CandidateCount: len(candidates),
	}
}

func collectCandidates(strategy EditMatchStrategy, content, search string) []candidate {
	switch strategy {
	case StrategyExact:
		return collectExactCandidates(content, search)
	case StrategyQuoteNormalized:
		return collectNormalizedCandidates(content, search, normalizeQuotes)
	case StrategyLineNumberPrefixStripped:
		return collectLineNumberPrefixCandidates(content, search)
	case StrategyEscapeNormalized:
		return collectEscapeNormalizedCandidates(content, search)
	case StrategyUnicodeEscapeNormalized:
		return collectUnicodeEscapeNormalizedCandidates(content, search)
	case StrategyLineTrimmed:
		return collectLineTrimmedCandidates(content, search)
	case StrategyIndentationFlexible:
		return collectIndentationFlexibleCandidates(content, search)
	case StrategyBlockAnchor:
		return collectBlockAnchorCandidates(content, search)
	}
	return nil
}

func collectExactCandidates(content, search string) []candidate {
	return collectSubstringCandidates(content, search)
}

func collectQuoteNormalizedCandidates(content, search string) []candidate {
	return collectNormalizedCandidates(content, search, normalizeQuotes)
}

var (
	colonLinePrefix = regexp.MustCompile(`^\d+: (.*)$`)
	tabLinePrefix   = regexp.MustCompile(`^\d+\t(.*)$`)
)

func collectLineNumberPrefixCandidates(content, search string) []candidate {
	stripped, ok := stripReadLineNumberPrefixes(search)
	if !ok || stripped == search {
		return nil
	}
	return collectSubstringCandidates(content, stripped)
}

func collectEscapeNormalizedCandidates(content, search string) []candidate {
	unescaped := unescapeVisibleCharacters(search)
	if unescaped == search {
		return nil
	}
	return collectSubstringCandidates(content, unescaped)
}

func collectUnicodeEscapeNormalizedCandidates(content, search string) []candidate {
	unescaped := unescapeUnicodeCharacters(search)
	if unescaped == search {
		return nil
	}
	return collectSubstringCandidates(content, unescaped)
}

func collectLineTrimmedCandidates(content, search string) []candidate {
	contentLines := strings.Split(content, "\n")
	searchLines := trimTrailingEmptyLine(strings.Split(search, "\n"))
	if len(searchLines) == 0 {
		return nil
	}
	var candidates []candidate
	for index := 0; index <= len(contentLines)-len(searchLines); index++ {
		block := contentLines[index : index+len(searchLines)]
		match := true
		for offset, line := range block {
			if strings.TrimSpace(line) != strings.TrimSpace(searchLines[offset]) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		candidates = append(candidates, blockCandidate(contentLines, index, len(searchLines)))
	}
	return candidates
}

func collectIndentationFlexibleCandidates(content, search string) []candidate {
	contentLines := strings.Split(content, "\n")
	searchLines := trimTrailingEmptyLine(strings.Split(search, "\n"))
	if len(searchLines) < 2 {
		return nil
	}
	normalizedSearch := removeCommonIndent(searchLines)
	var candidates []candidate
	for index := 0; index <= len(contentLines)-len(searchLines); index++ {
		block := contentLines[index : index+len(searchLines)]
		if removeCommonIndent(block) != normalizedSearch {
			continue
		}
		candidates = append(candidates, blockCandidate(contentLines, index, len(searchLines)))
	}
	return candidates
}

func collectBlockAnchorCandidates(content, search string) []candidate {
	contentLines := strings.Split(content, "\n")
	searchLines := trimTrailingEmptyLine(strings.Split(search, "\n"))
	if len(searchLines) < 3 {
		return nil
	}
	first := strings.TrimSpace(searchLines[0])
	last := strings.TrimSpace(searchLines[len(searchLines)-1])
	var candidates []candidate
	for index := 0; index <= len(contentLines)-len(searchLines); index++ {
		block := contentLines[index : index+len(searchLines)]
		if strings.TrimSpace(block[0]) != first {
			continue
		}
		if strings.TrimSpace(block[len(block)-1]) != last {
			continue
		}
		if averageMiddleSimilarity(block, searchLines) < blockAnchorMinSimilarity {
			continue
		}
		candidates = append(candidates, blockCandidate(contentLines, index, len(searchLines)))
	}
	return candidates
}

func collectSubstringCandidates(content, search string) []candidate {
	if len(search) == 0 {
		return nil
	}
	var candidates []candidate
	position := 0
	for position <= len(content) {
		index := strings.Index(content[position:], search)
		if index == -1 {
			break
		}
		index += position
		candidates = append(candidates, candidate{value: search, index: index})
		step := len(search)
		if step < 1 {
			step = 1
		}
		position = index + step
	}
	return candidates
}

// collectNormalizedCandidates mirrors collectNormalizedCandidates(): it searches
// the normalized content and slices the ORIGINAL string at the match position.
// Character indexing follows JS UTF-16 code units; quote normalization only
// touches BMP characters, so rune indexing is equivalent for the calls made here.
func collectNormalizedCandidates(content, search string, normalize func(string) string) []candidate {
	normalizedContent := []rune(normalize(content))
	normalizedSearch := []rune(normalize(search))
	contentRunes := []rune(content)
	searchLen := len([]rune(search))
	var candidates []candidate
	position := 0
	for position <= len(normalizedContent) {
		index := indexRunes(normalizedContent, normalizedSearch, position)
		if index == -1 {
			break
		}
		end := index + searchLen
		if end > len(contentRunes) {
			end = len(contentRunes)
		}
		candidates = append(candidates, candidate{value: string(contentRunes[index:end]), index: index})
		step := len(normalizedSearch)
		if step < 1 {
			step = 1
		}
		position = index + step
	}
	return candidates
}

func indexRunes(haystack, needle []rune, from int) int {
	if from > len(haystack) {
		return -1
	}
	if len(needle) == 0 {
		return from
	}
	for i := from; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func blockCandidate(lines []string, startLine, lineCount int) candidate {
	index := offsetForLine(lines, startLine)
	return candidate{
		value: strings.Join(lines[startLine:startLine+lineCount], "\n"),
		index: index,
	}
}

func offsetForLine(lines []string, lineIndex int) int {
	offset := 0
	for i := 0; i < lineIndex; i++ {
		offset += len([]rune(lines[i])) + 1
	}
	return offset
}

func trimTrailingEmptyLine(lines []string) []string {
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

var indentPrefix = regexp.MustCompile(`^[\t ]*`)

// removeCommonIndent mirrors removeCommonIndent().
func removeCommonIndent(lines []string) string {
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	if len(nonEmpty) == 0 {
		return strings.Join(lines, "\n")
	}
	minIndent := -1
	for _, line := range nonEmpty {
		n := len(indentPrefix.FindString(line))
		if minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			out[i] = line
			continue
		}
		out[i] = line[minIndent:]
	}
	return strings.Join(out, "\n")
}

func averageMiddleSimilarity(actual, expected []string) float64 {
	if len(actual) <= 2 {
		return 1
	}
	total := 0.0
	count := 0
	for i := 1; i < len(actual)-1; i++ {
		total += lineSimilarity(strings.TrimSpace(actual[i]), strings.TrimSpace(expected[i]))
		count++
	}
	if count == 0 {
		return 1
	}
	return total / float64(count)
}

func lineSimilarity(left, right string) float64 {
	if left == right {
		return 1
	}
	l := []rune(left)
	r := []rune(right)
	maxLength := len(l)
	if len(r) > maxLength {
		maxLength = len(r)
	}
	if maxLength == 0 {
		return 1
	}
	return 1 - float64(levenshtein(left, right))/float64(maxLength)
}

// normalizeQuotes mirrors normalizeQuotes().
func normalizeQuotes(value string) string {
	r := strings.NewReplacer(
		leftSingleCurly, "'",
		rightSingleCurly, "'",
		leftDoubleCurly, `"`,
		rightDoubleCurly, `"`,
	)
	return r.Replace(value)
}
