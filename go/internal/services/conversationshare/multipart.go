// multipart.go: the minimal multipart/form-data writer for uploadArtifact
// and the request-id generator for withRequestIdHeader. Kept hand-rolled and
// small (no new dependency): the wire shape is exactly two parts — the
// descriptor JSON field and the file part under its display name.
package conversationshare

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"strings"
)

type multipartWriter struct {
	buffer     io.Writer
	boundary   string
	contentTyp string
	closed     bool
}

func newMultipartWriter(buffer io.Writer) *multipartWriter {
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	boundary := "nexshare" + hex.EncodeToString(random)
	return &multipartWriter{
		buffer: buffer, boundary: boundary,
		contentTyp: "multipart/form-data; boundary=" + boundary,
	}
}

func (w *multipartWriter) contentType() string { return w.contentTyp }

func (w *multipartWriter) writeField(name, value string) {
	w.writePartHeader(name, "", "")
	_, _ = io.WriteString(w.buffer, value)
	w.writeBoundaryLine()
}

func (w *multipartWriter) writeFile(field, fileName, mimeType string, data []byte) {
	w.writePartHeader(field, fileName, mimeType)
	_, _ = w.buffer.Write(data)
	w.writeBoundaryLine()
}

func (w *multipartWriter) writePartHeader(name, fileName, mimeType string) {
	var builder strings.Builder
	builder.WriteString("--")
	builder.WriteString(w.boundary)
	builder.WriteString("\r\n")
	builder.WriteString(`Content-Disposition: form-data; name="`)
	builder.WriteString(escapeMultipartValue(name))
	builder.WriteString(`"`)
	if fileName != "" {
		builder.WriteString(`; filename="`)
		builder.WriteString(escapeMultipartValue(fileName))
		builder.WriteString(`"`)
	}
	builder.WriteString("\r\n")
	if mimeType != "" {
		builder.WriteString("Content-Type: ")
		builder.WriteString(mimeType)
		builder.WriteString("\r\n")
	}
	builder.WriteString("\r\n")
	_, _ = io.WriteString(w.buffer, builder.String())
}

func (w *multipartWriter) writeBoundaryLine() {
	_, _ = io.WriteString(w.buffer, "\r\n")
}

func (w *multipartWriter) close() {
	if w.closed {
		return
	}
	w.closed = true
	_, _ = io.WriteString(w.buffer, "--")
	_, _ = io.WriteString(w.buffer, w.boundary)
	_, _ = io.WriteString(w.buffer, "--\r\n")
}

// escapeMultipartValue escapes quote and backslash in disposition parameters.
func escapeMultipartValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", " ", "\n", " ")
	return replacer.Replace(value)
}

// newRequestID ports withRequestIdHeader's generator: a UUID-shaped opaque
// identifier (any [A-Za-z0-9._:-]{1,128} value round-trips).
func newRequestID() string {
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	random[6] = (random[6] & 0x0f) | 0x40
	random[8] = (random[8] & 0x3f) | 0x80
	return formatUUID(random)
}

func formatUUID(value []byte) string {
	hexed := hex.EncodeToString(value)
	var builder strings.Builder
	builder.WriteString(hexed[0:8])
	builder.WriteString("-")
	builder.WriteString(hexed[8:12])
	builder.WriteString("-")
	builder.WriteString(hexed[12:16])
	builder.WriteString("-")
	builder.WriteString(hexed[16:20])
	builder.WriteString("-")
	builder.WriteString(hexed[20:32])
	return builder.String()
}

// pathEscape percent-encodes a path segment (preparation ids / share codes).
func pathEscape(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' ||
			character == '.' || character == '~' {
			builder.WriteByte(character)
		} else {
			builder.WriteByte('%')
			builder.WriteString(strings.ToUpper(hex.EncodeToString([]byte{character})))
		}
	}
	return builder.String()
}
