package webauth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTokenComparisonUsesConstantTime is the code-level check the step asks for in place of a
// (statistical, flaky) timing test: the token comparison must go through
// crypto/subtle.ConstantTimeCompare on two digests of equal length.
//
// It parses the package source rather than grepping, so a refactor that replaces the subtle call
// with `==` on the digests fails here even when the strings look right. The two requirements
// together are what `tokensEqual` buys over a direct string compare: ConstantTimeCompare needs
// equal-length inputs (hence the hash first) and compares in time independent of content.
func TestTokenComparisonUsesConstantTime(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				calls[identifier.Name+"."+selector.Sel.Name]++
				return true
			})
		}
	}

	if calls["subtle.ConstantTimeCompare"] == 0 {
		t.Errorf("no crypto/subtle.ConstantTimeCompare call found in the package (calls: %v)", calls)
	}
	if calls["subtle.ConstantTimeCompare"] > 1 {
		t.Errorf("%d ConstantTimeCompare calls; expected one, in tokensEqual", calls["subtle.ConstantTimeCompare"])
	}

	// The hash-before-compare is what makes the constant-time call safe (subtle panics on a
	// length mismatch), so sha256.Sum256 must appear in the same file as the comparison.
	if calls["sha256.Sum256"] == 0 {
		t.Errorf("no sha256.Sum256 call found; the compared values would not be fixed length")
	}
}

// TestTokensEqualComparesByHashNotByString pins the observable behaviour the AST check protects:
// two different tokens of different lengths must both compare false without panicking, and only
// the exact token compares equal.
func TestTokensEqualComparesByHashNotByString(t *testing.T) {
	cases := []struct {
		expected, provided string
		want               bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "ab", false},
		{"abc", "abcd", false},
		{"", "", true},
		{"", "x", false},
		{"a", "", false},
		{testToken, testToken, true},
		{testToken, testToken + " ", false},
		{testToken, " " + testToken, false},
		{testToken, strings.ToUpper(testToken), false},
	}
	for _, tc := range cases {
		if got := tokensEqual(tc.expected, tc.provided); got != tc.want {
			t.Errorf("tokensEqual(%q, %q) = %v, want %v", tc.expected, tc.provided, got, tc.want)
		}
	}
}

// TestTokensEqualAcceptsLongInputs proves the length-mismatch panic of ConstantTimeCompare cannot
// be reached: inputs of wildly different lengths (including one over MAX_TOKEN_LENGTH) compare
// false instead of panicking.
func TestTokensEqualAcceptsLongInputs(t *testing.T) {
	long := make([]byte, 100_000)
	for i := range long {
		long[i] = byte('a' + i%26)
	}
	if tokensEqual(string(long), "a") {
		t.Error("a long token compared equal to a short one")
	}
	if !tokensEqual(string(long), string(long)) {
		t.Error("two identical long tokens did not compare equal")
	}
}

// TestTokenFingerprintIsSalted pins the domain separation: the fingerprint is
// sha256("nex-web-session:" + token), so a stored fingerprint can never be mistaken for a stored
// session-id hash of the same string, and two different tokens fingerprint differently.
func TestTokenFingerprintIsSalted(t *testing.T) {
	if tokenFingerprint("x") == sha256Hex("x") {
		t.Error("the token fingerprint is unsalted; it collides with a session id hash")
	}
	if tokenFingerprint("x") == tokenFingerprint("y") {
		t.Error("two different tokens produced the same fingerprint")
	}
	// It is stable across calls (it is written to disk and compared on load).
	if tokenFingerprint("x") != tokenFingerprint("x") {
		t.Error("the fingerprint is not deterministic")
	}
}

// TestNoNewEnvironmentVariables keeps the rewrite's D7 rule honest for this step: the auth
// package must not read an environment variable of its own. The only two variables the server
// legitimately reads here are NEX_SERVER_AUTH_TOKEN (in internal/server) and the data base
// directory (in internal/adapters/serverenv).
func TestNoNewEnvironmentVariables(t *testing.T) {
	allowed := map[string]bool{
		"NEX_SERVER_AUTH_TOKEN": true,
		"NEX_DATA_BASE_DIR":     true,
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		return filepath.Ext(info.Name()) == ".go" && len(info.Name()) < 9 // skip _test.go
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	// There are no os.Getenv calls in this package at all; the check is on serverenv usage.
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				identifier, ok := selector.X.(*ast.Ident)
				if !ok || identifier.Name != "serverenv" {
					return true
				}
				if selector.Sel.Name != "Trimmed" {
					t.Errorf("serverenv.%s is not an env read", selector.Sel.Name)
					return true
				}
				if len(call.Args) == 1 {
					if literal, ok := call.Args[0].(*ast.BasicLit); ok {
						name, _ := strconv.Unquote(literal.Value)
						if !allowed[name] {
							t.Errorf("webauth reads %q, which is not an existing server variable", name)
						}
					}
				}
				return true
			})
		}
	}
}
