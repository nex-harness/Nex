// petsroute.go ports the GET /api/pets/:id/spritesheet route
// (packages/server/src/http.ts lines 305-330; docs/specs/desktop-pets.md):
// content-type by extension (.png → image/png, everything else →
// image/webp), the `private, max-age=60` cache header, and the exact 404
// bodies. The route sits behind the /api/* auth middleware like every other
// /api route; path confinement is guaranteed by the pets service resolving
// inside the pets root (services/pets.GetPetSpritesheetPath).
package server

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// petsRoutePath is the route this file owns.
const petsRoutePath = "/api/pets/"

// pets route response bodies — exact strings, part of the wire contract.
const (
	petsNotFoundBody          = `{"error":"Pet not found"}`
	petsSpritesheetUnreadable = `{"error":"Spritesheet unreadable"}`
	petsServiceUnavailable    = `{"error":"Pets service unavailable"}`
)

// PetsService is the seam the server needs from the pets package
// (services.getOptional(IPetService) in the TS route).
type PetsService interface {
	GetPetSpritesheetPath(petID string) *struct{ Path string }
}

// servePetsSpritesheet handles GET /api/pets/:id/spritesheet. petID comes
// percent-decoded out of the path, exactly like the TS decodeURIComponent.
func servePetsSpritesheet(service PetsService, w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	escaped, ok := urlPathname(r)
	if !ok || !strings.HasPrefix(escaped, petsRoutePath) {
		return false
	}
	rest := strings.TrimPrefix(escaped, petsRoutePath)
	// Only the exact spritesheet suffix is owned; anything else under
	// /api/pets/ stays with the gateway (none exists today).
	if !strings.HasSuffix(rest, "/spritesheet") {
		return false
	}
	encodedID := strings.TrimSuffix(rest, "/spritesheet")
	petID, err := url.PathUnescape(encodedID)
	if err != nil {
		petID = encodedID
	}
	if service == nil {
		writePetsJSONError(w, http.StatusNotFound, petsServiceUnavailable)
		return true
	}
	result := service.GetPetSpritesheetPath(petID)
	if result == nil {
		writePetsJSONError(w, http.StatusNotFound, petsNotFoundBody)
		return true
	}
	content, readErr := os.ReadFile(result.Path)
	if readErr != nil {
		writePetsJSONError(w, http.StatusNotFound, petsSpritesheetUnreadable)
		return true
	}
	contentType := "image/webp"
	if strings.EqualFold(filepath.Ext(result.Path), ".png") {
		contentType = "image/png"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(content)
	}
	return true
}

func writePetsJSONError(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// petsContentTypeFor is the pure content-type decision, shared with the test
// table (extension-based; case-insensitive like the TS toLowerCase()).
func petsContentTypeFor(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".png") {
		return "image/png"
	}
	return "image/webp"
}
