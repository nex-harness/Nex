// Package pets is the IPetService port (step 3.8): the desktop-pets
// directory scan (Codex-compatible format) and spritesheet path resolution.
//
// Source: packages/services/src/pets/petService.ts + the shared
// packages/shared/src/pets/{petManifest,petFormat}.ts rules. Types
// (PetSummary/PetLoadError/...) are re-declared here the same way the other
// service packages port their @nex/shared types.
//
// The pet directory is {nexDataRoot}/pets/<id>/{pet.json, spritesheet.webp};
// ~/.codex is never read. All errors are per-directory (PetLoadError), a
// missing pets root is an empty list, not an error.
package pets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ChannelName is the rpc channel name (ServiceChannels.Pets).
const ChannelName = "pets"

// Manifest/format constants (petFormat.ts).
const (
	ManifestFileName                 = "pet.json"
	DefaultSpritesheetFileName       = "spritesheet.webp"
	FrameWidth                       = 192
	FrameHeight                      = 208
	FrameColumns                     = 8
	FrameRows                        = 9
	MaxFrames                        = 256
	MaxFps                           = 60
	DefaultSpritesheetWidth    int64 = 1536
	DefaultSpritesheetHeight   int64 = 1872
)

// FrameGrid ports PetFrameGrid.
type FrameGrid struct {
	Width   int `json:"width"`
	Height  int `json:"height"`
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// AnimationSpec ports PetAnimationSpec (permissive: unknown keys ignored,
// exactly like zod's object default).
type AnimationSpec struct {
	Frames   []int    `json:"frames"`
	Fps      *float64 `json:"fps,omitempty"`
	Loop     *bool    `json:"loop,omitempty"`
	Fallback string   `json:"fallback,omitempty"`
}

// Manifest ports PetManifest (the parsed pet.json).
type Manifest struct {
	ID              string                   `json:"id,omitempty"`
	DisplayName     string                   `json:"displayName,omitempty"`
	Description     string                   `json:"description,omitempty"`
	SpritesheetPath string                   `json:"spritesheetPath,omitempty"`
	Frame           *FrameGrid               `json:"frame,omitempty"`
	Animations      map[string]AnimationSpec `json:"animations,omitempty"`
}

// Summary ports PetSummary.
type Summary struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"displayName"`
	Description         string   `json:"description"`
	DirPath             string   `json:"dirPath"`
	SpritesheetFileName string   `json:"spritesheetFileName"`
	SpriteRows          int      `json:"spriteRows"`
	Manifest            Manifest `json:"manifest"`
}

// LoadError ports PetLoadError.
type LoadError struct {
	DirName string `json:"dirName"`
	Reason  string `json:"reason"`
}

// ListResult ports PetListResult.
type ListResult struct {
	Pets   []Summary   `json:"pets"`
	Errors []LoadError `json:"errors"`
}

// isDirectoryRelativePath ports isDirectoryRelativePath: not absolute, no ..
// segment, no drive letter.
func isDirectoryRelativePath(candidate string) bool {
	if candidate == "" {
		return false
	}
	if strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, `\`) {
		return false
	}
	if regexp.MustCompile(`^[A-Za-z]:[\\/]`).MatchString(candidate) {
		return false
	}
	for _, segment := range regexp.MustCompile(`[\\/]+`).Split(candidate, -1) {
		if segment == ".." || segment == "" {
			return false
		}
	}
	return true
}

// defaultFrameGrid ports PET_DEFAULT_FRAME_GRID.
func defaultFrameGrid() FrameGrid {
	return FrameGrid{Width: FrameWidth, Height: FrameHeight, Columns: FrameColumns, Rows: FrameRows}
}

// parseManifest ports parsePetManifest: schema plus the invariants
// (directory-relative spritesheetPath, frame count cap, animation frame
// bounds, fallback references). id defaults to the directory name.
func parseManifest(text, dirName string) (Manifest, string) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return Manifest{}, "pet.json 不是合法 JSON"
	}
	var manifest Manifest
	if id, ok := raw["id"]; ok {
		json.Unmarshal(id, &manifest.ID)
		if manifest.ID == "" {
			return Manifest{}, "pet.json 字段非法: id must be at least 1 character"
		}
	}
	if displayName, ok := raw["displayName"]; ok {
		json.Unmarshal(displayName, &manifest.DisplayName)
	}
	if description, ok := raw["description"]; ok {
		json.Unmarshal(description, &manifest.Description)
	}
	if path, ok := raw["spritesheetPath"]; ok {
		json.Unmarshal(path, &manifest.SpritesheetPath)
		if manifest.SpritesheetPath == "" {
			return Manifest{}, "pet.json 字段非法: spritesheetPath must be at least 1 character"
		}
	}
	if frame, ok := raw["frame"]; ok {
		var grid FrameGrid
		if err := json.Unmarshal(frame, &grid); err != nil || grid.Width <= 0 || grid.Height <= 0 || grid.Columns <= 0 || grid.Rows <= 0 {
			return Manifest{}, "pet.json 字段非法: invalid frame grid"
		}
		manifest.Frame = &grid
	}
	if animations, ok := raw["animations"]; ok {
		var parsed map[string]AnimationSpec
		if err := json.Unmarshal(animations, &parsed); err != nil {
			return Manifest{}, "pet.json 字段非法: invalid animations"
		}
		for name, spec := range parsed {
			if len(spec.Frames) == 0 {
				return Manifest{}, fmt.Sprintf("pet.json 字段非法: animation %s needs at least one frame", name)
			}
			if spec.Fps != nil && *spec.Fps > MaxFps {
				return Manifest{}, fmt.Sprintf("pet.json 字段非法: animation %s fps exceeds %d", name, MaxFps)
			}
		}
		manifest.Animations = parsed
	}

	spritesheetPath := manifest.SpritesheetPath
	if spritesheetPath == "" {
		spritesheetPath = DefaultSpritesheetFileName
	}
	if !isDirectoryRelativePath(spritesheetPath) {
		return Manifest{}, "spritesheetPath 必须是目录内相对路径（禁止绝对路径与 ..）"
	}
	grid := defaultFrameGrid()
	if manifest.Frame != nil {
		grid = *manifest.Frame
	}
	totalFrames := grid.Columns * grid.Rows
	if totalFrames > MaxFrames {
		return Manifest{}, fmt.Sprintf("帧网格超过上限 %d", MaxFrames)
	}
	for name, spec := range manifest.Animations {
		for _, frameIndex := range spec.Frames {
			if frameIndex < 0 || frameIndex >= totalFrames {
				return Manifest{}, fmt.Sprintf("动画 %s 引用了越界帧索引 %d", name, frameIndex)
			}
		}
		if spec.Fallback != "" {
			if _, ok := manifest.Animations[spec.Fallback]; !ok {
				return Manifest{}, fmt.Sprintf("动画 %s 的 fallback 指向不存在的动画 %s", name, spec.Fallback)
			}
		}
	}
	if manifest.ID == "" {
		manifest.ID = dirName
	}
	return manifest, ""
}

// readImageSize ports readImageSize: PNG and WebP (VP8/VP8L/VP8X) header
// parsing without a decode dependency.
func readImageSize(filePath string) (width, height int64, ok bool) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	header := make([]byte, 64)
	bytesRead, err := file.Read(header)
	if err != nil || bytesRead < 12 {
		return 0, 0, false
	}
	// PNG: 8-byte signature + IHDR (width/height big-endian at 16/20).
	if string(header[:8]) == "\x89PNG\r\n\x1a\n" {
		if bytesRead < 24 {
			return 0, 0, false
		}
		return int64(beUint32(header[16:20])), int64(beUint32(header[20:24])), true
	}
	// WebP: "RIFF" .... "WEBP" + VP8/VP8L/VP8X subchunk.
	if string(header[:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(header[12:16]) {
	case "VP8X":
		// 24-bit little-endian minus one, at offsets 24/27.
		if bytesRead < 30 {
			return 0, 0, false
		}
		return 1 + int64(leUint24(header[24:27])), 1 + int64(leUint24(header[27:30])), true
	case "VP8 ":
		// Lossy: frame header at 26; 16-bit little-endian (low 14 bits).
		if bytesRead < 30 {
			return 0, 0, false
		}
		return int64(leUint16(header[26:28]) & 0x3fff), int64(leUint16(header[28:30]) & 0x3fff), true
	case "VP8L":
		// Lossless: signature 0x2f then 4 packed bytes at 21 (14 bits each, minus one).
		if bytesRead < 26 || header[20] != 0x2f {
			return 0, 0, false
		}
		bits := leUint32(header[21:25])
		return int64(bits&0x3fff) + 1, int64((bits>>14)&0x3fff) + 1, true
	}
	return 0, 0, false
}

func beUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func leUint16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}
func leUint24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}
func leUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// isGridTilingImage ports isGridTilingImage: width must match exactly; height
// must be a multiple of the frame height and cover the declared rows
// (community pets extend rows below the default 9).
func isGridTilingImage(grid FrameGrid, imageWidth, imageHeight int64) bool {
	if int64(grid.Width*grid.Columns) != imageWidth {
		return false
	}
	if imageHeight%int64(grid.Height) != 0 {
		return false
	}
	return imageHeight/int64(grid.Height) >= int64(grid.Rows)
}

// Service ports IPetService.
type Service struct {
	rootDir string
	// 桌面端注入的 nex-media 授权通路；未注入（web server）时
	// getPetSpritesheetUrl 返回 nil，调用方回落静态路由。
	authorizeLocalMediaPreviewPath func(path string) (string, error)
	createLocalMediaPreviewURL     func(path string) string

	mu    sync.Mutex
	cache *cachedScan
}

type cachedScan struct {
	// signature: the root's name:mtimeMs list; rescan only when it changes.
	signature string
	result    ListResult
}

// Options ports PetServiceOptions.
type Options struct {
	// PetsRootDir overrides the pets root (tests). Empty = {paths}/pets.
	PetsRootDir string
	// Paths resolves the data root when PetsRootDir is empty.
	Paths interface{ NexDataRootDir() string }
	// Desktop-only nex-media authorization seam.
	AuthorizeLocalMediaPreviewPath func(path string) (string, error)
	CreateLocalMediaPreviewURL     func(path string) string
}

// New ports createPetService.
func New(options Options) *Service {
	rootDir := options.PetsRootDir
	if rootDir == "" && options.Paths != nil {
		rootDir = filepath.Join(options.Paths.NexDataRootDir(), "pets")
	}
	return &Service{
		rootDir:                        rootDir,
		authorizeLocalMediaPreviewPath: options.AuthorizeLocalMediaPreviewPath,
		createLocalMediaPreviewURL:     options.CreateLocalMediaPreviewURL,
	}
}

// ListPets ports listPets.
func (s *Service) ListPets() ListResult {
	result, _ := s.scan()
	return result
}

// RefreshPets ports refreshPets: clear the cache, then rescan (the root
// signature does not see deep manifest/spritesheet edits).
func (s *Service) RefreshPets() ListResult {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
	result, _ := s.scan()
	return result
}

// GetPetSpritesheetPath ports getPetSpritesheetPath: resolves a pet's
// spritesheet absolute path; nil when the pet or its spritesheet is missing.
// The returned path is always inside the pets root.
func (s *Service) GetPetSpritesheetPath(petID string) *struct{ Path string } {
	result, _ := s.scan()
	for _, pet := range result.Pets {
		if pet.ID != petID {
			continue
		}
		spritesheetPath := filepath.Join(pet.DirPath, pet.SpritesheetFileName)
		if !strings.HasPrefix(spritesheetPath, filepath.Clean(s.rootDir)+string(filepath.Separator)) {
			return nil
		}
		return &struct{ Path string }{Path: spritesheetPath}
	}
	return nil
}

// GetPetSpritesheetURL ports getPetSpritesheetUrl: desktop-only nex-media
// authorization; hosts without the seam return nil (the caller falls back to
// the server static route).
func (s *Service) GetPetSpritesheetURL(petID string) *struct{ URL string } {
	if s.authorizeLocalMediaPreviewPath == nil || s.createLocalMediaPreviewURL == nil {
		return nil
	}
	result := s.GetPetSpritesheetPath(petID)
	if result == nil {
		return nil
	}
	canonicalPath, err := s.authorizeLocalMediaPreviewPath(result.Path)
	if err != nil {
		return nil
	}
	return &struct{ URL string }{URL: s.createLocalMediaPreviewURL(canonicalPath)}
}

// scanRoot reads the pets root and builds the change signature
// (name:mtimeMs), so unchanged roots reuse the cached scan.
func (s *Service) scanRoot() (signature string, dirNames []string) {
	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		// 目录不存在/不可读：视为空（非错误）。
		return "", nil
	}
	var signatureParts []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirNames = append(dirNames, entry.Name())
		if info, err := os.Stat(filepath.Join(s.rootDir, entry.Name())); err == nil {
			signatureParts = append(signatureParts, fmt.Sprintf("%s:%d", entry.Name(), info.ModTime().UnixMilli()))
		} else {
			signatureParts = append(signatureParts, entry.Name()+":?")
		}
	}
	sort.Strings(dirNames)
	sort.Strings(signatureParts)
	return strings.Join(signatureParts, "|"), dirNames
}

func (s *Service) scan() (ListResult, bool) {
	signature, dirNames := s.scanRoot()
	s.mu.Lock()
	if s.cache != nil && s.cache.signature == signature {
		// 目录 mtime 不感知深层文件变化；manifest/精灵图级别的改动由
		// refreshPets() 显式清缓存覆盖（设置页「刷新」按钮）。
		result := s.cache.result
		s.mu.Unlock()
		return result, false
	}
	s.mu.Unlock()

	result := ListResult{Pets: []Summary{}, Errors: []LoadError{}}
	for _, dirName := range dirNames {
		if summary, loadErr, ok := s.scanOnePetDir(dirName); ok {
			result.Pets = append(result.Pets, summary)
		} else {
			result.Errors = append(result.Errors, loadErr)
		}
	}
	s.mu.Lock()
	s.cache = &cachedScan{signature: signature, result: result}
	s.mu.Unlock()
	return result, true
}

// scanOnePetDir ports scanOnePetDir: manifest → spritesheet bounds → grid
// tiling check. Failure is a LoadError, never an abort of the whole scan.
func (s *Service) scanOnePetDir(dirName string) (Summary, LoadError, bool) {
	dirPath := filepath.Join(s.rootDir, dirName)
	manifestText, err := os.ReadFile(filepath.Join(dirPath, ManifestFileName))
	if err != nil {
		return Summary{}, LoadError{DirName: dirName, Reason: "缺少 " + ManifestFileName}, false
	}
	manifest, reason := parseManifest(string(manifestText), dirName)
	if reason != "" {
		return Summary{}, LoadError{DirName: dirName, Reason: reason}, false
	}
	spritesheetFileName := manifest.SpritesheetPath
	if spritesheetFileName == "" {
		spritesheetFileName = DefaultSpritesheetFileName
	}
	// 防御：parseManifest 已拒绝绝对路径与 ..，这里再校验最终路径仍在目录内。
	spritesheetPath := filepath.Join(dirPath, spritesheetFileName)
	if !isDirectoryRelativePath(spritesheetFileName) ||
		!strings.HasPrefix(spritesheetPath, filepath.Clean(dirPath)+string(filepath.Separator)) {
		return Summary{}, LoadError{DirName: dirName, Reason: "spritesheetPath 越界"}, false
	}
	info, statErr := os.Stat(spritesheetPath)
	if statErr != nil || !info.Mode().IsRegular() {
		if statErr != nil {
			return Summary{}, LoadError{DirName: dirName, Reason: "精灵图缺失: " + spritesheetFileName}, false
		}
		return Summary{}, LoadError{DirName: dirName, Reason: "spritesheet 不是文件"}, false
	}
	width, height, ok := readImageSize(spritesheetPath)
	if !ok {
		return Summary{}, LoadError{DirName: dirName, Reason: "精灵图不是可识别的 PNG/WebP"}, false
	}
	grid := defaultFrameGrid()
	if manifest.Frame != nil {
		grid = *manifest.Frame
	}
	if !isGridTilingImage(grid, width, height) {
		return Summary{}, LoadError{DirName: dirName, Reason: fmt.Sprintf(
			"网格 %d×%d × %d×%d 未铺满图片 %d×%d", grid.Width, grid.Height, grid.Columns, grid.Rows, width, height)}, false
	}
	displayName := strings.TrimSpace(manifest.DisplayName)
	if displayName == "" {
		displayName = manifest.ID
	}
	if displayName == "" {
		displayName = dirName
	}
	return Summary{
		ID:                  manifest.ID,
		DisplayName:         displayName,
		Description:         manifest.Description,
		DirPath:             dirPath,
		SpritesheetFileName: spritesheetFileName,
		SpriteRows:          int(height / int64(grid.Height)),
		Manifest:            manifest,
	}, LoadError{}, true
}
