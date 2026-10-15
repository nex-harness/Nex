// petsroute_test.go (step 3.8): the /api/pets/:id/spritesheet route table —
// content-type by extension, the private 60s cache header, the exact 404
// bodies, percent-encoded ids, and the path-confinement guarantee (a pet
// whose spritesheet would resolve outside the pets root answers 404, never
// serves the file).
package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	petssvc "github.com/tylinux/nex/go/internal/services/pets"
)

func buildPetsHandler(t *testing.T) (http.Handler, *petssvc.Service, string) {
	t.Helper()
	root := t.TempDir()
	service := petssvc.New(petssvc.Options{PetsRootDir: root})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !servePetsSpritesheet(service, w, r) {
			writeNotFound(w)
		}
	})
	return mux, service, root
}

// routeWebP builds a minimal VP8X WebP carrying the default 1536×1872 grid,
// so the pets scan accepts it (the route tests only need a valid file).
func routeWebP(content []byte) []byte {
	// payload: chunk tag + size + flags(4) + width-1 (3LE) + height-1 (3LE)
	payload := []byte("VP8X")
	payload = append(payload, 10, 0, 0, 0, 0, 0, 0, 0)
	width, height := 1535, 1871
	payload = append(payload, byte(width), byte(width>>8), byte(width>>16))
	payload = append(payload, byte(height), byte(height>>8), byte(height>>16))
	payload = append(payload, content...)
	riff := append([]byte("WEBP"), payload...)
	buf := append([]byte("RIFF"), byte(len(riff)), byte(len(riff)>>8), byte(len(riff)>>16), byte(len(riff)>>24))
	return append(buf, riff...)
}

func writeRoutePet(t *testing.T, root, dirName, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "pet.json"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(dir, "spritesheet.webp"), routeWebP([]byte("GRID-BYTES")), 0o644)
	return dir
}

func TestPetsSpritesheetContentTypeTable(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/x/spritesheet.webp", "image/webp"},
		{"/x/spritesheet.WEBP", "image/webp"},
		{"/x/spritesheet.png", "image/png"},
		{"/x/spritesheet.PNG", "image/png"},
		{"/x/spritesheet", "image/webp"},
		{"/x/spritesheet.jpg", "image/webp"},
	}
	for _, tc := range cases {
		if got := petsContentTypeFor(tc.path); got != tc.want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestPetsSpritesheetServesWithHeaders(t *testing.T) {
	handler, _, root := buildPetsHandler(t)
	writeRoutePet(t, root, "cat", `{"id":"cat"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/cat/spritesheet", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "image/webp" {
		t.Fatalf("content-type = %q", contentType)
	}
	if cache := response.Header().Get("Cache-Control"); cache != "private, max-age=60" {
		t.Fatalf("cache-control = %q", cache)
	}
	if response.Body.String() != string(routeWebP([]byte("GRID-BYTES"))) {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestPetsSpritesheetNotFoundBody(t *testing.T) {
	handler, _, root := buildPetsHandler(t)
	writeRoutePet(t, root, "cat", `{"id":"cat"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/dog/spritesheet", nil))
	if response.Code != http.StatusNotFound || response.Body.String() != `{"error":"Pet not found"}` {
		t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
	}
}

func TestPetsSpritesheetUnreadableBody(t *testing.T) {
	handler, _, root := buildPetsHandler(t)
	writeRoutePet(t, root, "cat", `{"id":"cat"}`)
	// a directory spritesheet fails the pet validation → Pet not found
	writeRoutePet(t, root, "cat2", `{"id":"cat2"}`)
	os.Remove(filepath.Join(root, "cat2", "spritesheet.webp"))
	os.MkdirAll(filepath.Join(root, "cat2", "spritesheet.webp"), 0o755)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/cat2/spritesheet", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	// for the unreadable path: valid scan, then delete the file between the
	// scan and the read.
	dir3 := writeRoutePet(t, root, "cat3", `{"id":"cat3"}`)
	// force the scan to succeed and cache
	response3 := httptest.NewRecorder()
	handler.ServeHTTP(response3, httptest.NewRequest(http.MethodGet, "/api/pets/cat3/spritesheet", nil))
	if response3.Code != http.StatusOK {
		t.Fatalf("first fetch status = %d", response3.Code)
	}
	os.Remove(filepath.Join(dir3, "spritesheet.webp"))
	response4 := httptest.NewRecorder()
	handler.ServeHTTP(response4, httptest.NewRequest(http.MethodGet, "/api/pets/cat3/spritesheet", nil))
	// cached scan still resolves the path; the read now fails → unreadable
	if response4.Code != http.StatusNotFound || response4.Body.String() != `{"error":"Spritesheet unreadable"}` {
		t.Fatalf("status = %d body = %q", response4.Code, response4.Body.String())
	}
}

func TestPetsServiceUnavailableBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !servePetsSpritesheet(nil, w, r) {
			writeNotFound(w)
		}
	})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/cat/spritesheet", nil))
	if response.Code != http.StatusNotFound || response.Body.String() != `{"error":"Pets service unavailable"}` {
		t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
	}
}

func TestPetsSpritesheetPercentEncodedID(t *testing.T) {
	handler, _, root := buildPetsHandler(t)
	writeRoutePet(t, root, "my cat", `{"id":"my cat"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/my%20cat/spritesheet", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestPetsSpritesheetPathConfinement(t *testing.T) {
	handler, service, root := buildPetsHandler(t)
	// a manifest whose spritesheetPath escapes the pet dir is rejected at
	// parse (../), and a symlinked spritesheet is skipped by validation —
	// but the strongest guarantee is: the served file ALWAYS lives under
	// <root>/<petDir>. Plant a secret outside the pets root and a pet that
	// claims it.
	secretDir := filepath.Join(filepath.Dir(root), "outside")
	os.MkdirAll(secretDir, 0o755)
	secret := filepath.Join(secretDir, "secret.webp")
	os.WriteFile(secret, []byte("SECRET-BYTES"), 0o644)
	// evil manifests are rejected by parse — assert that none of the
	// traversal shapes can resolve.
	for _, manifest := range []string{
		`{"id":"e1","spritesheetPath":"../../outside/secret.webp"}`,
		`{"id":"e2","spritesheetPath":"/` + filepath.ToSlash(secret) + `"}`,
	} {
		writeRoutePet(t, root, "evil", manifest)
		if result := service.GetPetSpritesheetPath("e1"); result != nil {
			t.Fatalf("manifest %s resolved to %s", manifest, result.Path)
		}
		if result := service.GetPetSpritesheetPath("e2"); result != nil {
			t.Fatalf("manifest %s resolved to %s", manifest, result.Path)
		}
	}
	// and via HTTP the same manifests answer 404, never the secret bytes.
	writeRoutePet(t, root, "evil", `{"id":"e1","spritesheetPath":"../../outside/secret.webp"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/e1/spritesheet", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Body.String() == "SECRET-BYTES" {
		t.Fatal("the outside-root file was served")
	}
}

func TestPetsSpritesheetOtherMethodsAndPathsFallThrough(t *testing.T) {
	handler, _, _ := buildPetsHandler(t)
	// POST is not the route's method → falls through to notFound
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/pets/cat/spritesheet", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("POST status = %d", response.Code)
	}
	// a different subpath is not owned
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pets/cat/pet.json", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("pet.json status = %d", response.Code)
	}
	_ = fmt.Sprint()
}
