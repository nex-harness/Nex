package main

import (
	"strings"
)

func hasFlagName(args []string, name string) bool {
	dash := "-" + name
	doubleDash := "--" + name
	for _, a := range args {
		if a == dash || a == doubleDash ||
			strings.HasPrefix(a, dash+"=") || strings.HasPrefix(a, doubleDash+"=") {
			return true
		}
	}
	return false
}

func buildElevatedArgs(rawArgs []string) []string {
	out := make([]string, 0, len(rawArgs)+1)
	hasAttempted := false
	for _, a := range rawArgs {
		if a == "-no-uac" || a == "--no-uac" ||
			strings.HasPrefix(a, "-no-uac=") || strings.HasPrefix(a, "--no-uac=") {
			continue
		}
		if a == "-uac-attempted" || a == "--uac-attempted" ||
			strings.HasPrefix(a, "-uac-attempted=") || strings.HasPrefix(a, "--uac-attempted=") {
			hasAttempted = true
		}
		out = append(out, a)
	}
	if !hasAttempted {
		out = append(out, "-uac-attempted")
	}
	return out
}

func quoteWindowsArg(arg string) string {
	if arg == "" {
		return `""`
	}
	needsQuote := false
	for i := 0; i < len(arg); i++ {
		c := arg[i]
		if c == ' ' || c == '\t' || c == '"' || c == '\n' || c == '\v' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(arg); i++ {
		c := arg[i]
		if c == '\\' {
			backslashes++
			continue
		}
		if c == '"' {
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
			continue
		}
		if backslashes > 0 {
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
		}
		b.WriteByte(c)
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat(`\`, backslashes*2))
	}
	b.WriteByte('"')
	return b.String()
}

func buildWindowsCommandLine(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, quoteWindowsArg(a))
	}
	return strings.Join(parts, " ")
}

func buildAutostartCommandLine(exePath string, rawArgs []string) string {
	filtered := make([]string, 0, len(rawArgs)+1)
	filtered = append(filtered, exePath)
	for _, a := range rawArgs {
		if a == "-uac-attempted" || a == "--uac-attempted" ||
			strings.HasPrefix(a, "-uac-attempted=") || strings.HasPrefix(a, "--uac-attempted=") ||
			a == "-no-uac" || a == "--no-uac" ||
			strings.HasPrefix(a, "-no-uac=") || strings.HasPrefix(a, "--no-uac=") {
			continue
		}
		filtered = append(filtered, a)
	}
	return buildWindowsCommandLine(filtered)
}
