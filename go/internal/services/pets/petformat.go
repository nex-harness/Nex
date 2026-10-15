// petformat.go holds the pet-manifest parsing and the spritesheet image-size
// probing. Split out of pets.go so the service stays short; symbols unchanged.
package pets

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

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
