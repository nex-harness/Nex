package fsx

import (
	"bytes"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Encoding names mirror FileSystemTextEncoding on the Node side.
const (
	EncUTF8    = "utf8"
	EncUTF16LE = "utf16le"
	EncGB2312  = "gb2312"
	EncGBK     = "gbk"
	EncGB18030 = "gb18030"
)

var legacyChineseEncodings = []string{EncGB2312, EncGBK, EncGB18030}

// nonTextEncodings matches NON_TEXT_ENCODINGS in text-metadata.ts.
var nonTextEncodings = map[string]bool{"base64": true, "base64url": true, "hex": true}

var (
	utf8BOM    = []byte{0xef, 0xbb, 0xbf}
	utf16leBOM = []byte{0xff, 0xfe}
)

const (
	binaryControlByteThreshold    = 0.3
	maxDetectionTrailingDropBytes = 3
)

// detectTextEncoding mirrors detectTextEncoding(): BOM first, then a binary
// refusal, then UTF-8 validity, then the legacy Chinese candidates in order.
func detectTextEncoding(buffer []byte, path string) (string, error) {
	if len(buffer) >= 3 && bytes.HasPrefix(buffer, utf8BOM) {
		return EncUTF8, nil
	}
	if len(buffer) >= 2 && bytes.HasPrefix(buffer, utf16leBOM) {
		return EncUTF16LE, nil
	}
	if looksLikeBinary(buffer) {
		return "", unsupportedEncodingError(path)
	}
	if utf8.Valid(buffer) {
		return EncUTF8, nil
	}
	for _, enc := range legacyChineseEncodings {
		if roundTripsWithOptionalTrailingDrop(buffer, enc) {
			return enc, nil
		}
	}
	return "", unsupportedEncodingError(path)
}

// decodeTextBuffer decodes with an explicit encoding or detects one. The BOM is
// left in the output, exactly as Node's Buffer.toString does.
func decodeTextBuffer(buffer []byte, path string) (string, string, error) {
	encoding, err := detectTextEncoding(buffer, path)
	if err != nil {
		return "", "", err
	}
	content, err := decodeWithEncoding(buffer, encoding)
	if err != nil {
		return "", "", err
	}
	return content, encoding, nil
}

func decodeWithEncoding(buffer []byte, encoding string) (string, error) {
	switch encoding {
	case EncUTF8:
		return string(buffer), nil
	case EncUTF16LE:
		return decodeUTF16LE(buffer), nil
	}
	if isLegacyChineseEncoding(encoding) {
		return decodeLegacy(buffer, encoding)
	}
	return string(buffer), nil
}

// decodeUTF16LE mirrors Node Buffer.toString("utf16le"): it does NOT strip a BOM
// and replaces an unpaired trailing byte by dropping it (Node ignores a lone byte).
func decodeUTF16LE(b []byte) string {
	n := len(b) / 2
	u16 := make([]uint16, n)
	for i := 0; i < n; i++ {
		u16[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	runes := utf16.Decode(u16)
	return string(runes)
}

func decodeLegacy(b []byte, encoding string) (string, error) {
	dec := legacyDecoder(encoding)
	out, _, err := transform.Bytes(dec, b)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func legacyDecoder(encoding string) transform.Transformer {
	switch encoding {
	case EncGB2312:
		// iconv-lite's gb2312 is a GBK-compatible table for the bytes both accept;
		// Go has no plain GB2312 codec, and GBK decodes every GB2312 sequence.
		return simplifiedchinese.GBK.NewDecoder()
	case EncGBK:
		return simplifiedchinese.GBK.NewDecoder()
	default:
		return simplifiedchinese.GB18030.NewDecoder()
	}
}

func legacyEncoder(encoding string) transform.Transformer {
	switch encoding {
	case EncGB2312:
		return simplifiedchinese.GBK.NewEncoder()
	case EncGBK:
		return simplifiedchinese.GBK.NewEncoder()
	default:
		return simplifiedchinese.GB18030.NewEncoder()
	}
}

func roundTripsWithOptionalTrailingDrop(buffer []byte, encoding string) bool {
	if len(buffer) == 0 {
		return true
	}
	max := maxDetectionTrailingDropBytes
	if len(buffer) < max {
		max = len(buffer)
	}
	for drop := 0; drop <= max; drop++ {
		var candidate []byte
		if drop == 0 {
			candidate = buffer
		} else {
			candidate = buffer[:len(buffer)-drop]
		}
		if len(candidate) == 0 {
			continue
		}
		decoded, err := decodeLegacy(candidate, encoding)
		if err != nil {
			continue
		}
		encoded, err := encodeLegacy(decoded, encoding)
		if err != nil {
			continue
		}
		if bytes.Equal(encoded, candidate) {
			return true
		}
	}
	return false
}

func encodeLegacy(content, encoding string) ([]byte, error) {
	out, _, err := transform.Bytes(legacyEncoder(encoding), []byte(content))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func looksLikeBinary(buffer []byte) bool {
	if len(buffer) == 0 {
		return false
	}
	controlBytes := 0
	for _, b := range buffer {
		if b == 0 {
			return true
		}
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			controlBytes++
		}
	}
	return float64(controlBytes)/float64(len(buffer)) > binaryControlByteThreshold
}

func isLegacyChineseEncoding(encoding string) bool {
	for _, e := range legacyChineseEncodings {
		if e == encoding {
			return true
		}
	}
	return false
}

func shouldNormalizeLineEndings(encoding string) bool {
	return !nonTextEncodings[strings.ToLower(encoding)]
}

func unsupportedEncodingError(path string) error {
	suffix := ""
	if path != "" {
		suffix = ": " + path
	}
	return &Error{Code: "unsupported", Path: path, Message: "Unsupported or binary text encoding" + suffix}
}

// detectLineEndings mirrors detectLineEndings(): CRLF when the file has more
// CRLF newlines than lone LFs.
func detectLineEndings(content string) string {
	crlf, lf := 0, 0
	for i := 0; i < len(content); i++ {
		if content[i] != '\n' {
			continue
		}
		if i > 0 && content[i-1] == '\r' {
			crlf++
		} else {
			lf++
		}
	}
	if crlf > lf {
		return "CRLF"
	}
	return "LF"
}

// normalizeLineEndings mirrors normalizeLineEndings(): replace CRLF with LF.
func normalizeLineEndings(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

// applyRequestedLineEndings mirrors applyRequestedLineEndings().
func applyRequestedLineEndings(content, lineEndings string) string {
	normalized := normalizeLineEndings(content)
	if lineEndings == "CRLF" {
		return strings.ReplaceAll(normalized, "\n", "\r\n")
	}
	return normalized
}

// encodeTextContent mirrors encodeTextContent(): utf8 (default) or utf16le via
// Buffer.from(content, encoding). The BOM, when present in the decoded content,
// is re-encoded as bytes just like Node does.
func encodeTextContent(content, encoding string) ([]byte, error) {
	switch encoding {
	case EncUTF16LE:
		runes := []rune(content)
		u16 := utf16.Encode(runes)
		out := make([]byte, len(u16)*2)
		for i, v := range u16 {
			out[2*i] = byte(v)
			out[2*i+1] = byte(v >> 8)
		}
		return out, nil
	case EncUTF8, "":
		return []byte(content), nil
	}
	if isLegacyChineseEncoding(encoding) {
		return encodeLegacy(content, encoding)
	}
	return []byte(content), nil
}
