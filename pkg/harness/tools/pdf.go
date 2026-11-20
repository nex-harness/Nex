package tools

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

func isPDFFile(path string, data []byte) bool {
	if len(data) >= 5 && bytes.Equal(data[:5], []byte("%PDF-")) {
		return true
	}
	return strings.EqualFold(filepath.Ext(path), ".pdf")
}

// extractPDFText extracts readable text from a PDF file using `pdftotext` when available
// (best fidelity for CJK/CID fonts), falling back to a pure-Go PDF stream text parser
// (`FlateDecode` + `BT...ET` text objects) so `CGO_ENABLED=0` static binaries always work.
func extractPDFText(ctx context.Context, absPath string, data []byte) (string, int) {
	if pdftotextPath, err := exec.LookPath("pdftotext"); err == nil && pdftotextPath != "" {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(tctx, pdftotextPath, "-layout", absPath, "-")
		if out, err := cmd.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			text := strings.ReplaceAll(string(out), "\r\n", "\n")
			pages := strings.Split(text, "\f")
			var sb strings.Builder
			pageCount := 0
			for _, p := range pages {
				trimmed := strings.TrimSpace(p)
				if trimmed == "" {
					continue
				}
				pageCount++
				fmt.Fprintf(&sb, "--- Page %d ---\n%s\n\n", pageCount, trimmed)
			}
			if pageCount > 0 {
				return strings.TrimSpace(sb.String()), pageCount
			}
		}
	}
	return extractPDFTextPureGo(data)
}

var (
	rePDFStream = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\n?endstream`)
	rePDFBTET   = regexp.MustCompile(`(?s)\bBT\b(.*?)\bET\b`)
)

func extractPDFTextPureGo(data []byte) (string, int) {
	matches := rePDFStream.FindAllSubmatch(data, -1)
	var pages []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		streamBytes := m[1]
		// Attempt zlib inflate (FlateDecode); if not compressed, use raw stream bytes
		decoded := streamBytes
		if zr, err := zlib.NewReader(bytes.NewReader(streamBytes)); err == nil {
			if raw, rErr := io.ReadAll(io.LimitReader(zr, 2*1024*1024)); rErr == nil && len(raw) > 0 {
				decoded = raw
			}
			_ = zr.Close()
		}
		if !bytes.Contains(decoded, []byte("BT")) {
			continue
		}
		var pageBuf strings.Builder
		for _, bt := range rePDFBTET.FindAllSubmatch(decoded, -1) {
			if len(bt) < 2 {
				continue
			}
			extracted := extractPDFOperators(bt[1])
			if extracted != "" {
				if pageBuf.Len() > 0 {
					pageBuf.WriteByte('\n')
				}
				pageBuf.WriteString(extracted)
			}
		}
		if txt := strings.TrimSpace(pageBuf.String()); txt != "" {
			pages = append(pages, txt)
		}
	}

	if len(pages) == 0 {
		return "(PDF contains no extractable text layer — it may be a scanned image PDF.)", 0
	}
	var sb strings.Builder
	for i, p := range pages {
		fmt.Fprintf(&sb, "--- Page %d ---\n%s\n\n", i+1, p)
	}
	return strings.TrimSpace(sb.String()), len(pages)
}

func extractPDFOperators(bt []byte) string {
	var sb strings.Builder
	i := 0
	n := len(bt)
	for i < n {
		switch bt[i] {
		case '(':
			// Literal PDF string `(...)` with escape handling
			i++
			depth := 1
			var lit []byte
			for i < n && depth > 0 {
				c := bt[i]
				if c == '\\' && i+1 < n {
					i++
					switch bt[i] {
					case 'n':
						lit = append(lit, '\n')
					case 'r':
						lit = append(lit, '\r')
					case 't':
						lit = append(lit, '\t')
					case '(', ')', '\\':
						lit = append(lit, bt[i])
					default:
						lit = append(lit, bt[i])
					}
					i++
					continue
				}
				if c == '(' {
					depth++
					lit = append(lit, c)
				} else if c == ')' {
					depth--
					if depth > 0 {
						lit = append(lit, c)
					}
				} else {
					lit = append(lit, c)
				}
				i++
			}
			sb.WriteString(decodePDFStringBytes(lit))
		case '<':
			if i+1 < n && bt[i+1] == '<' {
				i += 2
				continue
			}
			// Hex string `<FEFF...>`
			i++
			start := i
			for i < n && bt[i] != '>' {
				i++
			}
			hexRaw := bytes.TrimSpace(bt[start:i])
			if i < n {
				i++
			}
			if decoded := decodePDFHexString(hexRaw); decoded != "" {
				sb.WriteString(decoded)
			}
		case 'T':
			if i+1 < n && (bt[i+1] == '*' || bt[i+1] == 'd' || bt[i+1] == 'D') {
				if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
					sb.WriteByte('\n')
				}
				i += 2
				continue
			}
			i++
		default:
			i++
		}
	}
	return strings.TrimSpace(sb.String())
}

func decodePDFStringBytes(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF {
		return decodeUTF16BE(b[2:])
	}
	var out strings.Builder
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c <= 0x7E) {
			out.WriteByte(c)
		}
	}
	return out.String()
}

func decodePDFHexString(hexBytes []byte) string {
	var clean []byte
	for _, c := range hexBytes {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			clean = append(clean, c)
		}
	}
	if len(clean) < 2 {
		return ""
	}
	if len(clean)%2 == 1 {
		clean = append(clean, '0')
	}
	raw := make([]byte, len(clean)/2)
	for i := 0; i < len(clean); i += 2 {
		raw[i/2] = hexVal(clean[i])<<4 | hexVal(clean[i+1])
	}
	return decodePDFStringBytes(raw)
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

func decodeUTF16BE(b []byte) string {
	u16 := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u16 = append(u16, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u16))
}
