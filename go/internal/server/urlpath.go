package server

import (
	"net/http"
	"strings"
)

// urlPathname reproduces `new URL(c.req.url).pathname` for an origin-form request target.
//
// Go's net/url hands back an already-decoded r.URL.Path and keeps the raw form in
// r.URL.RawPath, but Node computes the pathname from the *escaped* target and resolves dot
// segments there, so the two disagree on exactly the cases that matter here:
//
//	/%2e%2e/etc/passwd    Node: URL sees "%2e%2e" as a dot segment and collapses it -> /etc/passwd
//	Go r.URL.Path:            already decoded to /../etc/passwd, no segments left to collapse
//	/..%2fetc%2fpasswd    Node: "%2f" is not a separator, so the segment survives -> /../etc/passwd
//	Go r.URL.Path:            already decoded to /../etc/passwd
//
// The recorded TypeScript responses confirm the split (see go/testdata/http/requests.json):
// the %2e%2e forms answer 200 with index.html, the %2f form answers 404. So the pathname must
// be rebuilt from the escaped target, with WHATWG "single dot"/"double dot" segment removal.
func urlPathname(r *http.Request) (string, bool) {
	escaped := r.URL.EscapedPath()
	if escaped == "" {
		return "/", true
	}
	return removeDotSegments(escaped), true
}

// removeDotSegments implements the WHATWG URL path parser's dot-segment removal, which is what
// makes `new URL()` collapse "..", ".", and their percent-encoded spellings. Segments are
// compared after percent-decoding, matching the spec's "double-dot path segment" check.
//
// The output stays percent-encoded: decodeURIComponent runs afterwards, exactly as in
// resolveStaticFile, which is what turns a surviving %2f into a separator only for the
// filesystem join and never for the fallback decision.
func removeDotSegments(escaped string) string {
	// Fast path: nothing to collapse. Kept explicit because the common case must not allocate
	// a segment slice per request.
	if !strings.Contains(escaped, "%2e") && !strings.Contains(escaped, "%2E") &&
		!strings.Contains(escaped, "/./") && !strings.Contains(escaped, "/../") &&
		escaped != "." && escaped != ".." &&
		!strings.HasSuffix(escaped, "/.") && !strings.HasSuffix(escaped, "/..") {
		return escaped
	}

	trailingSlash := strings.HasSuffix(escaped, "/")
	segments := strings.Split(escaped, "/")
	out := make([]string, 0, len(segments))
	// segments[0] is the empty string before the leading slash.
	out = append(out, segments[0])
	for _, segment := range segments[1:] {
		switch {
		case isSingleDotSegment(segment):
			trailingSlash = true
		case isDoubleDotSegment(segment):
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			trailingSlash = true
		default:
			out = append(out, segment)
			trailingSlash = false
		}
	}
	normalized := strings.Join(out, "/")
	if trailingSlash && !strings.HasSuffix(normalized, "/") {
		normalized += "/"
	}
	return normalized
}

// isSingleDotSegment is the WHATWG "single-dot path segment": ".", "%2e" in any case, or a dot
// percent-encoded on either side.
func isSingleDotSegment(segment string) bool {
	return segment == "." || isEncodedDot(segment) || isMixedDot(segment)
}

// isDoubleDotSegment is the WHATWG "double-dot path segment": "..", ".%2e"/"%2e." or "%2e%2e",
// case-insensitive on the escape.
func isDoubleDotSegment(segment string) bool {
	if segment == ".." {
		return true
	}
	if isEncodedDot(segment) {
		return false
	}
	if segment == "%2e%2e" || segment == "%2E%2e" || segment == "%2e%2E" || segment == "%2E%2E" {
		return true
	}
	return isMixedDot(segment)
}

// isEncodedDot reports whether the whole segment is a single percent-encoded dot.
func isEncodedDot(segment string) bool {
	return len(segment) == 3 && segment[0] == '%' && segment[1] == '2' &&
		(segment[2] == 'e' || segment[2] == 'E')
}

// isMixedDot reports the half-encoded spellings: a literal '.' paired with "%2e" in either
// order ("%2e." and ".%2e"). Both are single-dot segments; "%2e." is also the prefix of a
// double-dot spelling, which isDoubleDotSegment handles.
func isMixedDot(segment string) bool {
	if len(segment) != 4 {
		return false
	}
	if segment[0] == '.' {
		return isEncodedDot(segment[1:])
	}
	return segment[0] == '%' && segment[1] == '2' &&
		(segment[2] == 'e' || segment[2] == 'E') && segment[3] == '.'
}

// writeBadRequest matches the plain-text shape used for every non-2xx written here.
func writeBadRequest(w http.ResponseWriter) {
	writePlain(w, http.StatusBadRequest, "400 Bad Request")
}
