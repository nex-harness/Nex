// multipart_test.go (step 3.8): the hand-rolled multipart writer the upload
// endpoint needs. The wire shape is exactly two parts — the descriptor JSON
// field and the file part under its display name — so a drift here would be
// invisible server-side until every upload failed to parse.
package conversationshare

import (
	"bytes"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMultipartWriterRoundTripsThroughTheStandardParser(t *testing.T) {
	form := &bytes.Buffer{}
	writer := newMultipartWriter(form)
	writer.writeField("descriptor", `{"artifact_id":"share-artifact-1"}`)
	writer.writeFile("file", "report.pdf", "application/pdf", []byte("%PDF-1.4\nbody\x00\xff"))
	writer.close()

	contentType := writer.contentType()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("content type %q does not parse: %v", contentType, err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q", mediaType)
	}
	if params["boundary"] == "" {
		t.Fatal("boundary missing")
	}

	reader := multipart.NewReader(bytes.NewReader(form.Bytes()), params["boundary"])
	form1, err := reader.NextPart()
	if err != nil {
		t.Fatalf("read descriptor part: %v", err)
	}
	if form1.FormName() != "descriptor" {
		t.Fatalf("first field = %q, want descriptor", form1.FormName())
	}
	descriptor := readAll(t, form1)
	if descriptor != `{"artifact_id":"share-artifact-1"}` {
		t.Fatalf("descriptor payload = %q", descriptor)
	}

	form2, err := reader.NextPart()
	if err != nil {
		t.Fatalf("read file part: %v", err)
	}
	if form2.FormName() != "file" {
		t.Fatalf("second field = %q, want file", form2.FormName())
	}
	if form2.FileName() != "report.pdf" {
		t.Fatalf("file name = %q", form2.FileName())
	}
	if got := form2.Header.Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("part content type = %q", got)
	}
	payload := readAllBytes(t, form2)
	// Binary content must survive byte-for-byte (no encoding mangling).
	if !bytes.Equal(payload, []byte("%PDF-1.4\nbody\x00\xff")) {
		t.Fatalf("file payload = %q", payload)
	}
	if _, err := reader.NextPart(); err == nil {
		t.Fatal("expected exactly two parts")
	}
}

func TestMultipartWriterIsolatesFieldsWithCRLF(t *testing.T) {
	// A filename carrying a CRLF must not be able to inject a header.
	form := &bytes.Buffer{}
	writer := newMultipartWriter(form)
	writer.writeFile("file", "evil\r\nX-Injected: 1\r\n\r\n.pdf", "application/pdf", []byte("x"))
	writer.close()

	mediaType, params, err := mime.ParseMediaType(writer.contentType())
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type broken: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(form.Bytes()), params["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		t.Fatalf("read part: %v", err)
	}
	if strings.ContainsAny(part.FileName(), "\r\n") {
		t.Fatalf("filename still carries CRLF after parsing: %q", part.FileName())
	}
	if part.Header.Get("X-Injected") != "" {
		t.Fatalf("header injection succeeded: %v", part.Header)
	}
}

func TestMultipartWriterEscapesQuotesAndBackslashes(t *testing.T) {
	form := &bytes.Buffer{}
	writer := newMultipartWriter(form)
	writer.writeFile("file", `we"ird\name.pdf`, "application/pdf", []byte("x"))
	writer.close()
	if !strings.Contains(form.String(), `filename="we\"ird\\name.pdf"`) {
		t.Fatalf("disposition escaping wrong:\n%s", form.String())
	}
}

func TestEscapeMultipartValueNeutralizesLineBreaks(t *testing.T) {
	// CR/LF become spaces (the FormData encoding rule), quotes and backslashes
	// are backslash-escaped.
	if got := escapeMultipartValue("a\r\nb"); got != "a  b" {
		t.Fatalf("line break escaping = %q", got)
	}
	if got := escapeMultipartValue(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("quote escaping = %q", got)
	}
}

func TestPathEscapeMatchesEncodeURIComponent(t *testing.T) {
	cases := map[string]string{
		"prep-1":       "prep-1",
		"a b":          "a%20b",
		"a/b":          "a%2Fb",
		"share?code=1": "share%3Fcode%3D1",
		"ünïcode":      "%C3%BCn%C3%AFcode",
		"~-._":         "~-._",
		"a+b":          "a%2Bb",
		"pre%par":      "pre%25par",
	}
	for input, want := range cases {
		if got := pathEscape(input); got != want {
			t.Errorf("pathEscape(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNewRequestIDIsUUIDShapedAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		id := newRequestID()
		if !safeRequestIDRe.MatchString(id) {
			t.Fatalf("request id %q is not a safe header value", id)
		}
		if len(id) != 36 || id[14] != '4' || (id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b') {
			t.Fatalf("request id %q is not a v4 UUID", id)
		}
		for _, r := range id {
			if !utf8.ValidRune(r) {
				t.Fatalf("request id has invalid rune")
			}
		}
		if seen[id] {
			t.Fatalf("duplicate request id %q", id)
		}
		seen[id] = true
	}
}

func TestContentTypeHeaderCarriesTheBoundary(t *testing.T) {
	writer := newMultipartWriter(&bytes.Buffer{})
	contentType := writer.contentType()
	if !strings.HasPrefix(contentType, "multipart/form-data; boundary=nexshare") {
		t.Fatalf("content type = %q", contentType)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", contentType)
	if header.Get("Content-Type") != contentType {
		t.Fatal("content type is not header-safe")
	}
}

func readAll(t *testing.T, part *multipart.Part) string {
	t.Helper()
	return string(readAllBytes(t, part))
}

func readAllBytes(t *testing.T, part *multipart.Part) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(part); err != nil {
		t.Fatalf("read part body: %v", err)
	}
	return buffer.Bytes()
}
