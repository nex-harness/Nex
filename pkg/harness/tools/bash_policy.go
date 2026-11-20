package tools

import (
	"strings"
)

// IsReadOnlyBashCommand inspects a shell command string (including pipelines and
// boolean chains like `&&`, `||`, `;`) and returns true ONLY when every segment is
// provably a read-only inspection command with no redirections, command substitutions,
// or mutating flags.
func IsReadOnlyBashCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}

	// Reject any command substitution, subshells, process substitution, backgrounding, or redirections.
	if hasUnsafeShellMetachar(trimmed) {
		return false
	}

	segments, ok := splitShellChain(trimmed)
	if !ok || len(segments) == 0 {
		return false
	}

	for _, seg := range segments {
		tokens, ok := tokenizeShellSegment(seg)
		if !ok || len(tokens) == 0 {
			return false
		}
		if !isReadOnlySegment(tokens) {
			return false
		}
	}
	return true
}

func hasUnsafeShellMetachar(s string) bool {
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if inSingle {
			continue
		}
		// Inside double quotes, backticks and $(...) still execute commands!
		if ch == '`' {
			return true
		}
		if ch == '$' && i+1 < len(s) && s[i+1] == '(' {
			return true
		}
		if inDouble {
			continue
		}
		// Outside quotes: reject redirections, subshells, newlines, and standalone '&'
		switch ch {
		case '>', '<', '(', ')', '{', '}', '\n', '\r':
			return true
		case '&':
			if i+1 < len(s) && s[i+1] == '&' {
				i++ // allow &&
				continue
			}
			return true
		}
	}
	return inSingle || inDouble
}

func splitShellChain(s string) ([]string, bool) {
	var segments []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		seg := strings.TrimSpace(cur.String())
		if seg != "" {
			segments = append(segments, seg)
		}
		cur.Reset()
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			cur.WriteByte(ch)
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			cur.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			cur.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if ch == ';' {
				flush()
				continue
			}
			if ch == '|' {
				if i+1 < len(s) && s[i+1] == '|' {
					i++
				}
				flush()
				continue
			}
			if ch == '&' && i+1 < len(s) && s[i+1] == '&' {
				i++
				flush()
				continue
			}
		}
		cur.WriteByte(ch)
	}
	if inSingle || inDouble {
		return nil, false
	}
	flush()
	return segments, true
}

func tokenizeShellSegment(seg string) ([]string, bool) {
	var tokens []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	hasToken := false

	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			hasToken = true
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			hasToken = true
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			hasToken = true
			continue
		}
		if !inSingle && !inDouble && (ch == ' ' || ch == '\t') {
			if hasToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				hasToken = false
			}
			continue
		}
		cur.WriteByte(ch)
		hasToken = true
	}
	if inSingle || inDouble {
		return nil, false
	}
	if hasToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, true
}

var simpleReadOnlyCommands = map[string]bool{
	"ls":       true,
	"pwd":      true,
	"cat":      true,
	"head":     true,
	"tail":     true,
	"wc":       true,
	"stat":     true,
	"file":     true,
	"which":    true,
	"whereis":  true,
	"type":     true,
	"echo":     true,
	"printf":   true,
	"uname":    true,
	"whoami":   true,
	"id":       true,
	"date":     true,
	"env":      true,
	"printenv": true,
	"tree":     true,
	"du":       true,
	"df":       true,
	"ps":       true,
	"rg":       true,
	"grep":     true,
	"egrep":    true,
	"fgrep":    true,
	"ag":       true,
	"fd":       true,
	"diff":     true,
	"cmp":      true,
	"sort":     true,
	"uniq":     true,
	"cut":      true,
	"tr":       true,
	"jq":       true,
	"realpath": true,
	"dirname":  true,
	"basename": true,
	"readlink": true,
	"md5":      true,
	"md5sum":   true,
	"shasum":   true,
	"sha256sum": true,
	"true":     true,
	"false":    true,
}

func isReadOnlySegment(tokens []string) bool {
	// Skip leading KEY=VALUE environment assignments (e.g. `LC_ALL=C git status`)
	idx := 0
	for idx < len(tokens) && isEnvAssign(tokens[idx]) {
		idx++
	}
	if idx >= len(tokens) {
		return false
	}
	bin := tokens[idx]
	args := tokens[idx+1:]

	// Normalize /usr/bin/git -> git
	if slash := strings.LastIndex(bin, "/"); slash >= 0 {
		bin = bin[slash+1:]
	}

	if simpleReadOnlyCommands[bin] {
		// Guard against `sort -o file` writing to disk
		if bin == "sort" {
			for _, a := range args {
				if a == "-o" || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output") {
					return false
				}
			}
		}
		return true
	}

	switch bin {
	case "find":
		return isReadOnlyFind(args)
	case "sed":
		// sed is read-only only without -i / --in-place
		for _, a := range args {
			if a == "-i" || strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place") {
				return false
			}
		}
		return true
	case "git":
		return isReadOnlyGit(args)
	case "go":
		return isReadOnlyGo(args)
	case "gh":
		return isReadOnlyGH(args)
	}
	return false
}

func isEnvAssign(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := tok[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

func isReadOnlyFind(args []string) bool {
	for _, a := range args {
		switch a {
		case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
			return false
		}
	}
	return true
}

func isReadOnlyGit(args []string) bool {
	// Skip global git flags like `-C dir`, `--no-pager`, `-P`, `--git-dir=...`
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "-c" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return false
	}
	sub := args[i]
	subArgs := args[i+1:]

	switch sub {
	case "status", "diff", "log", "show", "rev-parse", "ls-files", "ls-tree",
		"describe", "shortlog", "blame", "grep", "merge-base", "rev-list", "cat-file", "reflog":
		// Reject `-o` / `--output` write flags
		for _, a := range subArgs {
			if a == "-o" || strings.HasPrefix(a, "--output") {
				return false
			}
		}
		return true
	case "branch":
		// Only allow listing branches, reject create/delete/rename/copy/move
		for _, a := range subArgs {
			switch a {
			case "-d", "-D", "-m", "-M", "-c", "-C", "--delete", "--move", "--copy", "-f", "--force", "--set-upstream-to", "--unset-upstream":
				return false
			}
			// Positional branch name without listing flag creates a branch!
			if !strings.HasPrefix(a, "-") {
				return false
			}
		}
		return true
	case "tag":
		if len(subArgs) == 0 {
			return true
		}
		for _, a := range subArgs {
			if a == "-l" || a == "--list" || a == "-v" || a == "--verify" {
				return true
			}
		}
		return false
	case "remote":
		if len(subArgs) == 0 {
			return true
		}
		return subArgs[0] == "-v" || subArgs[0] == "--verbose" || subArgs[0] == "show" || subArgs[0] == "get-url"
	case "config":
		for _, a := range subArgs {
			if a == "--get" || a == "--get-all" || a == "--list" || a == "-l" {
				return true
			}
		}
		return false
	}
	return false
}

func isReadOnlyGo(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version", "env", "list", "doc":
		for _, a := range args[1:] {
			if a == "-w" { // go env -w mutates config
				return false
			}
		}
		return true
	}
	return false
}

func isReadOnlyGH(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "pr", "issue", "run", "release", "repo":
		switch args[1] {
		case "list", "view", "status", "diff", "checks":
			return true
		}
	case "auth":
		return args[1] == "status"
	}
	return false
}
