// fault.go ports packages/services/src/fs/fsFaultInjection.ts (step 3.1).
//
// The fault injector lets a test make a specific filesystem operation fail at a
// specific path, so crash-safety can be tested without a real crash. Rules are
// configured in Go (the TS also parses them from NEX_E2E_FS_FAULTS; the Go port
// takes the same rule structs directly — no new env vars, and the E2E harness
// builds them itself).
//
// It is safe for concurrent use: the process-wide injector is behind a mutex and
// the hit counters are per-rule state the mutex covers.
package fsx

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// FsFaultOperation is the operation tag a rule matches on. The set mirrors the
// TS one; the sqlite*/stream entries exist in the TS for other modules and are
// accepted here so rule files port unchanged.
type FsFaultOperation string

const (
	FaultAny         FsFaultOperation = "any"
	FaultAppendFile  FsFaultOperation = "appendFile"
	FaultMkdir       FsFaultOperation = "mkdir"
	FaultOpen        FsFaultOperation = "open"
	FaultReadFile    FsFaultOperation = "readFile"
	FaultReaddir     FsFaultOperation = "readdir"
	FaultRename      FsFaultOperation = "rename"
	FaultRm          FsFaultOperation = "rm"
	FaultStat        FsFaultOperation = "stat"
	FaultWriteFile   FsFaultOperation = "writeFile"
	FaultStreamWrite FsFaultOperation = "createWriteStream"
	FaultSqliteOpen  FsFaultOperation = "sqliteOpen"
	FaultSqliteRun   FsFaultOperation = "sqliteRun"
)

var supportedOperations = map[FsFaultOperation]bool{
	FaultAny: true, FaultAppendFile: true, FaultMkdir: true, FaultOpen: true,
	FaultReadFile: true, FaultReaddir: true, FaultRename: true, FaultRm: true,
	FaultStat: true, FaultWriteFile: true, FaultStreamWrite: true,
	FaultSqliteOpen: true, FaultSqliteRun: true,
}

// FsFaultRuleConfig is one rule, mirroring FsFaultRuleConfig.
type FsFaultRuleConfig struct {
	ID           string
	Code         string
	Operations   []FsFaultOperation // nil/empty means "any"
	PathIncludes string
	PathEndsWith string
	PathRegex    string
	MaxMatches   int // 0 means unlimited; default 1
	Message      string
}

// FsFaultCheckInput is the check call: what operation on what path.
type FsFaultCheckInput struct {
	Operation FsFaultOperation
	Path      string
}

// FsFaultHit records one injected fault.
type FsFaultHit struct {
	ID         string
	Code       string
	Operation  FsFaultOperation
	Path       string
	MatchIndex int
	MatchedAt  time.Time
}

// FaultError is the injected error: it carries an errno-style Code so the
// retry logic in atomic.go can treat it as non-retryable (isInjectedFsFaultError
// in the TS checks nexFsFaultId; here it is a distinct Go type).
type FaultError struct {
	ID        string
	Code      string
	Operation FsFaultOperation
	Path      string
	Message   string
}

func (e *FaultError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("Injected fs fault %s for %s: %s", e.Code, e.Operation, e.Path)
}

// IsInjectedFsFaultError ports isInjectedFsFaultError.
func IsInjectedFsFaultError(err error) bool {
	var faultErr *FaultError
	return asFaultError(err, &faultErr)
}

func asFaultError(err error, target **FaultError) bool {
	for err != nil {
		if f, ok := err.(*FaultError); ok {
			*target = f
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// normalizedFaultRule is a validated rule with match state.
type normalizedFaultRule struct {
	code         string
	id           string
	maxMatches   int
	message      string
	operations   map[FsFaultOperation]bool
	pathEndsWith string
	pathIncludes string
	pathRegex    *regexp.Regexp
	matchedCount int
}

// Injector is the FsFaultInjector interface. maybeThrow returns the *FaultError
// to propagate (Go spelling of a TS throw) or nil.
type Injector interface {
	IsEnabled() bool
	MaybeThrow(input FsFaultCheckInput) error
	GetHits() []FsFaultHit
	Reset()
}

type injector struct {
	mu    sync.Mutex
	rules []*normalizedFaultRule
	hits  []FsFaultHit
}

// NewFaultInjector ports createFsFaultInjector. Invalid rules panic with the
// same messages the TS throws (the TS throws at normalize time too).
func NewFaultInjector(rules []FsFaultRuleConfig) Injector {
	normalized := make([]*normalizedFaultRule, 0, len(rules))
	for i, rule := range rules {
		normalized = append(normalized, normalizeFaultRule(rule, i))
	}
	return &injector{rules: normalized}
}

func normalizeFaultRule(input FsFaultRuleConfig, index int) *normalizedFaultRule {
	trimNonEmpty := func(value, field string) string {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			panic(fmt.Sprintf("Invalid fs fault rule at index %d: %s must be a non-empty string", index, field))
		}
		return trimmed
	}
	id := trimNonEmpty(input.ID, "id")
	code := trimNonEmpty(input.Code, "code")

	ops := map[FsFaultOperation]bool{}
	if len(input.Operations) == 0 {
		ops[FaultAny] = true
	} else {
		for _, op := range input.Operations {
			if !supportedOperations[op] {
				panic(fmt.Sprintf("Invalid fs fault rule at index %d: unsupported operation %s", index, op))
			}
			ops[op] = true
		}
	}

	maxMatches := input.MaxMatches
	if maxMatches < 0 {
		panic(fmt.Sprintf("Invalid fs fault rule at index %d: maxMatches must be a non-negative integer", index))
	}
	if maxMatches == 0 {
		// The TS default is 1 (parseMaxMatches: undefined → 1). Explicit
		// unlimited is negative in Go but the TS rejects negatives, so 0 stays
		// the "default 1" spelling. Callers wanting more passes set MaxMatches.
		maxMatches = 1
	}

	pathIncludes := normalizeFaultPathPattern(input.PathIncludes)
	pathEndsWith := normalizeFaultPathPattern(input.PathEndsWith)
	var pathRegex *regexp.Regexp
	if input.PathRegex != "" {
		compiled, err := regexp.Compile(normalizeFaultPathPattern(input.PathRegex))
		if err != nil {
			panic(fmt.Sprintf("Invalid fs fault rule at index %d: pathRegex is invalid: %v", index, err))
		}
		pathRegex = compiled
	}
	return &normalizedFaultRule{
		code: code, id: id, maxMatches: maxMatches, message: input.Message,
		operations: ops, pathIncludes: pathIncludes, pathEndsWith: pathEndsWith,
		pathRegex: pathRegex,
	}
}

// normalizeFaultPathPattern mirrors normalizePathForFaultMatch: backslashes
// become forward slashes so one rule file works on both platforms.
func normalizeFaultPathPattern(pattern string) string {
	return strings.ReplaceAll(pattern, `\`, "/")
}

func faultOperationMatches(rule *normalizedFaultRule, operation FsFaultOperation) bool {
	return rule.operations[FaultAny] || rule.operations[operation]
}

func faultPathMatches(rule *normalizedFaultRule, path string) bool {
	normalized := normalizeFaultPathPattern(path)
	if rule.pathIncludes != "" && !strings.Contains(normalized, rule.pathIncludes) {
		return false
	}
	if rule.pathEndsWith != "" && !strings.HasSuffix(normalized, rule.pathEndsWith) {
		return false
	}
	if rule.pathRegex != nil && !rule.pathRegex.MatchString(normalized) {
		return false
	}
	return true
}

func faultHasRemainingMatches(rule *normalizedFaultRule) bool {
	// A negative maxMatches (from a negative MaxMatches input, rejected at
	// normalize) never reaches here; maxMatches 0 was normalized to 1 to match
	// the TS default, and "unlimited" is expressed by MaxMatches < 0 in config
	// — which normalizeFaultRule rejects, so rules here are always bounded.
	return rule.matchedCount < rule.maxMatches
}

func (in *injector) IsEnabled() bool {
	return len(in.rules) > 0
}

func (in *injector) MaybeThrow(input FsFaultCheckInput) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, rule := range in.rules {
		if !faultHasRemainingMatches(rule) || !faultOperationMatches(rule, input.Operation) || !faultPathMatches(rule, input.Path) {
			continue
		}
		rule.matchedCount++
		hit := FsFaultHit{
			Code: rule.code, ID: rule.id, MatchIndex: rule.matchedCount,
			MatchedAt: time.Now(), Operation: input.Operation, Path: input.Path,
		}
		in.hits = append(in.hits, hit)
		return &FaultError{
			ID: rule.id, Code: rule.code, Operation: input.Operation,
			Path: input.Path, Message: rule.message,
		}
	}
	return nil
}

func (in *injector) GetHits() []FsFaultHit {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]FsFaultHit, len(in.hits))
	copy(out, in.hits)
	return out
}

func (in *injector) Reset() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.hits = nil
	for _, rule := range in.rules {
		rule.matchedCount = 0
	}
}

// ---- process-wide injector (getProcessFsFaultInjector / setForTests) ----------

var (
	processInjectorMu sync.Mutex
	processInjector   Injector
	testInjector      Injector
)

// SetFaultInjectorForTests ports setFsFaultInjectorForTests: install an
// injector (nil to clear) that takes precedence over the process one.
func SetFaultInjectorForTests(in Injector) {
	processInjectorMu.Lock()
	defer processInjectorMu.Unlock()
	testInjector = in
}

// ProcessFaultInjector ports getProcessFsFaultInjector: the test override first,
// then the lazily built process one (no env parsing — the Go server process does
// not enable fault injection from the environment; the E2E harness passes rules
// through SetFaultInjectorForTests).
func ProcessFaultInjector() Injector {
	processInjectorMu.Lock()
	defer processInjectorMu.Unlock()
	if testInjector != nil {
		return testInjector
	}
	if processInjector == nil {
		processInjector = NewFaultInjector(nil)
	}
	return processInjector
}

// MaybeThrowInjectedFsFault ports maybeThrowInjectedFsFault.
func MaybeThrowInjectedFsFault(input FsFaultCheckInput) error {
	return ProcessFaultInjector().MaybeThrow(input)
}
