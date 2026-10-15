// pets_test.go (step 3.8): the IPetService behavior suite — manifest parsing
// and validation, image-size header parsing (PNG/WebP variants), the grid
// tiling check, per-directory error collection, cache/refresh semantics, and
// the spritesheet path confinement.
package pets

import (
	"os"
	"path/filepath"
	"testing"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	return New(Options{PetsRootDir: filepath.Join(root, "pets")}), filepath.Join(root, "pets")
}

// webpFile builds a minimal WebP container: RIFF + size + "WEBP" + chunk tag
// + chunk size + payload, so the payload starts at file offset 20 exactly
// like a real file (the parser reads VP8X at 24, VP8 at 26, VP8L's 0x2f at 20).
func webpFile(t *testing.T, path string, chunk string, extra []byte) {
	t.Helper()
	payload := append([]byte(chunk), itole(len(extra))...)
	payload = append(payload, extra...)
	riff := append([]byte("WEBP"), payload...)
	buf := append([]byte("RIFF"), itole(len(riff))...)
	buf = append(buf, riff...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func itole(v int) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }

func le24(v int) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16)} }

func le16(v int) []byte { return []byte{byte(v), byte(v >> 8)} }

func pngFile(t *testing.T, path string, width, height int) {
	t.Helper()
	buf := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	// IHDR chunk: length + type + width(4BE) + height(4BE)
	buf = append(buf, 0, 0, 0, 13, 'I', 'H', 'D', 'R')
	buf = append(buf, byte(width>>24), byte(width>>16), byte(width>>8), byte(width))
	buf = append(buf, byte(height>>24), byte(height>>16), byte(height>>8), byte(height))
	buf = append(buf, 8, 0, 0, 0, 0, 0, 0, 0, 0) // bit depth etc + CRC (unparsed)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDefaultPet(t *testing.T, petsRoot, dirName string) string {
	t.Helper()
	dir := filepath.Join(petsRoot, dirName)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(`{"id":"`+dirName+`"}`), 0o644)
	// VP8X: 4 bytes of flags/padding then width-1 and height-1 as 24-bit LE
	// (file offsets 24/27) → 1536×1872, the default grid.
	extra := append([]byte{0, 0, 0, 0}, append(le24(1535), le24(1871)...)...)
	webpFile(t, filepath.Join(dir, DefaultSpritesheetFileName), "VP8X", extra)
	return dir
}

func TestListPetsHappyPath(t *testing.T) {
	service, root := newService(t)
	writeDefaultPet(t, root, "cat")
	result := service.ListPets()
	if len(result.Errors) != 0 {
		t.Fatalf("errors = %v", result.Errors)
	}
	if len(result.Pets) != 1 {
		t.Fatalf("pets = %+v", result.Pets)
	}
	pet := result.Pets[0]
	if pet.ID != "cat" || pet.DisplayName != "cat" || pet.DirPath != filepath.Join(root, "cat") {
		t.Fatalf("pet = %+v", pet)
	}
	if pet.SpriteRows != 9 {
		t.Fatalf("spriteRows = %d, want 9 (1872/208)", pet.SpriteRows)
	}
	if pet.SpritesheetFileName != DefaultSpritesheetFileName {
		t.Fatalf("spritesheetFileName = %s", pet.SpritesheetFileName)
	}
}

func TestListPetsMissingRootIsEmpty(t *testing.T) {
	service, _ := newService(t)
	result := service.ListPets()
	if len(result.Pets) != 0 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want empty", result)
	}
}

func TestListPetsPerDirectoryErrors(t *testing.T) {
	service, root := newService(t)
	// empty dir: missing pet.json
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	// bad JSON
	bad := filepath.Join(root, "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, ManifestFileName), []byte("{oops"), 0o644)
	// valid manifest but missing spritesheet
	nosheet := filepath.Join(root, "nosheet")
	os.MkdirAll(nosheet, 0o755)
	os.WriteFile(filepath.Join(nosheet, ManifestFileName), []byte(`{"id":"nosheet"}`), 0o644)
	// unrecognized image format
	badsheet := filepath.Join(root, "badsheet")
	os.MkdirAll(badsheet, 0o755)
	os.WriteFile(filepath.Join(badsheet, ManifestFileName), []byte(`{"id":"badsheet"}`), 0o644)
	os.WriteFile(filepath.Join(badsheet, DefaultSpritesheetFileName), []byte("GIF89a...."), 0o644)
	// grid mismatch: image 100×100
	mismatch := filepath.Join(root, "mismatch")
	os.MkdirAll(mismatch, 0o755)
	os.WriteFile(filepath.Join(mismatch, ManifestFileName), []byte(`{"id":"mismatch"}`), 0o644)
	extra := append([]byte{0, 0, 0, 0}, append(le24(99), le24(99)...)...)
	webpFile(t, filepath.Join(mismatch, DefaultSpritesheetFileName), "VP8X", extra)

	result := service.ListPets()
	if len(result.Pets) != 0 {
		t.Fatalf("pets = %+v, want none", result.Pets)
	}
	if len(result.Errors) != 5 {
		t.Fatalf("errors = %v", result.Errors)
	}
	reasons := map[string]string{}
	for _, e := range result.Errors {
		reasons[e.DirName] = e.Reason
	}
	if reasons["empty"] != "缺少 pet.json" {
		t.Errorf("empty reason = %q", reasons["empty"])
	}
	if reasons["bad"] != "pet.json 不是合法 JSON" {
		t.Errorf("bad reason = %q", reasons["bad"])
	}
	if reasons["nosheet"] != "精灵图缺失: spritesheet.webp" {
		t.Errorf("nosheet reason = %q", reasons["nosheet"])
	}
	if reasons["badsheet"] != "精灵图不是可识别的 PNG/WebP" {
		t.Errorf("badsheet reason = %q", reasons["badsheet"])
	}
}

func TestParseManifestGuards(t *testing.T) {
	cases := []struct{ name, json, want string }{
		{"absolute path", `{"id":"x","spritesheetPath":"/etc/passwd"}`, "spritesheetPath 必须是目录内相对路径（禁止绝对路径与 ..）"},
		{"parent escape", `{"id":"x","spritesheetPath":"../other.png"}`, "spritesheetPath 必须是目录内相对路径（禁止绝对路径与 ..）"},
		{"frame over cap", `{"id":"x","frame":{"width":192,"height":208,"columns":16,"rows":17}}`, "帧网格超过上限 256"},
		{"oob frame index", `{"id":"x","animations":{"idle":{"frames":[999]}}}`, "动画 idle 引用了越界帧索引 999"},
		{"missing fallback", `{"id":"x","animations":{"a":{"frames":[0],"fallback":"nope"}}}`, "动画 a 的 fallback 指向不存在的动画 nope"},
		{"empty frames", `{"id":"x","animations":{"a":{"frames":[]}}}`, "pet.json 字段非法: animation a needs at least one frame"},
	}
	for _, tc := range cases {
		if _, reason := parseManifest(tc.json, "dir"); reason != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, reason, tc.want)
		}
	}
	// id defaults to dirName (codex model.rs behavior).
	manifest, reason := parseManifest(`{}`, "dog")
	if reason != "" || manifest.ID != "dog" {
		t.Fatalf("manifest = %+v reason = %q", manifest, reason)
	}
}

func TestParseManifestFpsCap(t *testing.T) {
	fps := 61.0
	_ = fps
	// fps>60 → zod max error path
	if _, reason := parseManifest(`{"id":"x","animations":{"a":{"frames":[0],"fps":61}}}`, "d"); reason == "" {
		t.Fatal("fps 61 must be rejected")
	}
	if _, reason := parseManifest(`{"id":"x","animations":{"a":{"frames":[0],"fps":60}}}`, "d"); reason != "" {
		t.Fatalf("fps 60 must pass: %q", reason)
	}
}

func TestReadImageSizeVariants(t *testing.T) {
	dir := t.TempDir()
	// VP8L: signature 0x2f + packed 14-bit (w-1, h-1)
	// w=1536, h=1872: bits = (1872-1)<<14 | (1536-1)
	bits := uint32(1871)<<14 | 1535
	webpL := []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24), 0, 0}
	webpFile(t, filepath.Join(dir, "l.webp"), "VP8L", webpL)
	w, h, ok := readImageSize(filepath.Join(dir, "l.webp"))
	if !ok || w != 1536 || h != 1872 {
		t.Fatalf("VP8L size = %d×%d ok=%v", w, h, ok)
	}
	// VP8 lossy: 16-bit LE (low 14 bits) at payload offset 6/8 → file 26/28
	vp8 := append([]byte{0, 0, 0, 0, 0, 0}, le16(1535)...)
	vp8 = append(vp8, le16(1871)...)
	webpFile(t, filepath.Join(dir, "v.webp"), "VP8 ", vp8)
	w, h, ok = readImageSize(filepath.Join(dir, "v.webp"))
	if !ok || w != 1535&0x3fff || h != 1871&0x3fff {
		t.Fatalf("VP8 size = %d×%d ok=%v", w, h, ok)
	}
	// PNG
	pngFile(t, filepath.Join(dir, "p.png"), 1536, 1872)
	w, h, ok = readImageSize(filepath.Join(dir, "p.png"))
	if !ok || w != 1536 || h != 1872 {
		t.Fatalf("PNG size = %d×%d ok=%v", w, h, ok)
	}
	// v2 spritesheet (1536×2288 = 11 rows) must tile
	if !isGridTilingImage(defaultFrameGrid(), 1536, 2288) {
		t.Fatal("v2 11-row spritesheet must tile under the default grid")
	}
	if isGridTilingImage(defaultFrameGrid(), 1537, 1872) {
		t.Fatal("width mismatch must not tile")
	}
}

func TestRefreshPetsSeesDeepChanges(t *testing.T) {
	service, root := newService(t)
	dir := writeDefaultPet(t, root, "cat")
	if result := service.ListPets(); len(result.Pets) != 1 {
		t.Fatalf("pets = %+v", result.Pets)
	}
	// break the manifest deep inside the dir (root mtime unchanged on Linux
	// for content-only edits): cached scan still returns the pet.
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte("{broken"), 0o644)
	if result := service.ListPets(); len(result.Pets) != 1 {
		t.Fatalf("cache must hide deep edits until refresh: %+v", result)
	}
	// refresh clears the cache and sees the error.
	if result := service.RefreshPets(); len(result.Errors) != 1 {
		t.Fatalf("refresh errors = %v", result.Errors)
	}
}

func TestGetPetSpritesheetPathConfinement(t *testing.T) {
	service, root := newService(t)
	writeDefaultPet(t, root, "cat")
	if result := service.GetPetSpritesheetPath("cat"); result == nil ||
		result.Path != filepath.Join(root, "cat", DefaultSpritesheetFileName) {
		t.Fatalf("path result = %+v", result)
	}
	if service.GetPetSpritesheetPath("dog") != nil {
		t.Fatal("unknown pet must yield nil")
	}
	// a manifest with a traversal-shaped spritesheetPath cannot resolve
	// outside the pets root.
	evil := filepath.Join(root, "evil")
	os.MkdirAll(evil, 0o755)
	os.WriteFile(filepath.Join(evil, ManifestFileName), []byte(`{"id":"evil","spritesheetPath":"../cat/spritesheet.webp"}`), 0o644)
	if service.GetPetSpritesheetPath("evil") != nil {
		t.Fatal("traversal pet must not resolve (parse rejects ../)")
	}
}

func TestGetPetSpritesheetURLRequiresDesktopSeam(t *testing.T) {
	service, root := newService(t)
	writeDefaultPet(t, root, "cat")
	if service.GetPetSpritesheetURL("cat") != nil {
		t.Fatal("web host (no auth seam) must return nil")
	}
	authorized := New(Options{
		PetsRootDir:                    root,
		AuthorizeLocalMediaPreviewPath: func(path string) (string, error) { return "media:" + path, nil },
		CreateLocalMediaPreviewURL:     func(path string) string { return "nex-media://" + path },
	})
	if result := authorized.GetPetSpritesheetURL("cat"); result == nil || result.URL != "nex-media://media:"+filepath.Join(root, "cat", DefaultSpritesheetFileName) {
		t.Fatalf("url = %+v", result)
	}
}

func TestRowExtendedSpritesheet(t *testing.T) {
	service, root := newService(t)
	dir := filepath.Join(root, "v2pet")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(`{"id":"v2pet"}`), 0o644)
	// 1536×2288 → 11 rows
	extra := append([]byte{0, 0, 0, 0}, append(le24(1535), le24(2287)...)...)
	webpFile(t, filepath.Join(dir, DefaultSpritesheetFileName), "VP8X", extra)
	result := service.ListPets()
	if len(result.Errors) != 0 || len(result.Pets) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Pets[0].SpriteRows != 11 {
		t.Fatalf("spriteRows = %d, want 11", result.Pets[0].SpriteRows)
	}
}
