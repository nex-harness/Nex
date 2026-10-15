// archive.go: the plugin-sync tar.gz archive codec, porting
// pluginSyncArchive.ts. Same security posture as the skills archive (entry
// names flow through NormalizeSyncRelativePath, extracted bytes capped, only
// file/dir entries accepted) with two plugin-specific differences: the
// archive carries a `.nex-plugin-sync.json` metadata entry, and file modes
// from the tar header are restored (plugin scripts need the exec bit).
package plugins

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/skills"
)

const (
	tarBlockSize = 512
	tarEndBytes  = tarBlockSize * 2
	// MetadataArchivePath is the in-archive metadata entry
	// (PLUGIN_SYNC_METADATA_ARCHIVE_PATH).
	MetadataArchivePath = ".nex-plugin-sync.json"
	// ArchivePathLabel is the error label for plugin archive entry names.
	ArchivePathLabel = "unsafe plugin archive path"
)

// ArchiveMetadata is the `.nex-plugin-sync.json` payload.
type ArchiveMetadata struct {
	Plugins            []ArchivePluginMetadata    `json:"plugins"`
	MarketplaceSources []ArchiveMarketplaceSource `json:"marketplaceSources,omitempty"`
}

// ArchivePluginMetadata is one plugin entry of the archive metadata.
type ArchivePluginMetadata struct {
	Name          string `json:"name"`
	PluginID      string `json:"pluginId"`
	DirectoryName string `json:"directoryName"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

// ArchiveMarketplaceSource is one mirrored marketplace source entry.
type ArchiveMarketplaceSource struct {
	MarketplaceID string `json:"marketplaceId"`
	DirectoryName string `json:"directoryName"`
}

// ArchiveEntry is either a sourcePath→archivePath copy or inline content.
type ArchiveEntry struct {
	SourcePath  string
	ArchivePath string
	Content     []byte // when set, written verbatim instead of SourcePath
	Mode        os.FileMode
}

// ArchiveExceedsLimitError marks the byte-cap errors (the TS string errors
// `plugin sync archive exceeds limit: a/b`).
type ArchiveExceedsLimitError struct {
	ActualBytes int64
	MaxBytes    int64
}

func (e *ArchiveExceedsLimitError) Error() string {
	return fmt.Sprintf("plugin sync archive exceeds limit: %d/%d", e.ActualBytes, e.MaxBytes)
}

// CreateSyncArchive builds the tar.gz archive: the entries in order, then
// the metadata entry, then the end blocks (createPluginSyncArchive).
func CreateSyncArchive(entries []ArchiveEntry, metadata ArchiveMetadata) ([]byte, error) {
	var buf bytes.Buffer
	for _, entry := range entries {
		archivePath, err := skills.NormalizeSyncRelativePath(entry.ArchivePath, ArchivePathLabel)
		if err != nil {
			return nil, err
		}
		if entry.Content != nil {
			appendTarFile(&buf, archivePath, entry.Content, entry.Mode)
		} else if err := appendTarEntry(&buf, entry.SourcePath, archivePath); err != nil {
			return nil, err
		}
	}
	metadataJSON, err := marshalIndentedNL(metadata)
	if err != nil {
		return nil, err
	}
	appendTarFile(&buf, MetadataArchivePath, metadataJSON, 0o644)
	buf.Write(make([]byte, tarEndBytes))

	var out bytes.Buffer
	gzipWriter := gzip.NewWriter(&out)
	if _, err := gzipWriter.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ExtractSyncArchiveOptions caps extraction.
type ExtractSyncArchiveOptions struct {
	MaxExtractedBytes int64
}

// ExtractSyncArchive extracts under targetDir with mode restoration.
func ExtractSyncArchive(archive []byte, targetDir string, options ExtractSyncArchiveOptions) error {
	targetRoot, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	var limit int64 = -1
	if options.MaxExtractedBytes > 0 {
		limit = options.MaxExtractedBytes + tarEndBytes + tarBlockSize*512
	}
	data, err := readAllWithLimit(gzipReader, limit)
	if err != nil {
		return err
	}

	var extractedBytes int64
	offset := 0
	for offset+tarBlockSize <= len(data) {
		header := data[offset : offset+tarBlockSize]
		offset += tarBlockSize
		if isAllZero(header) {
			break
		}
		size, err := readTarOctal(header, 124, 12)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid plugin sync archive entry size")
		}
		typeFlag := readTarString(header, 156, 157)
		if typeFlag == "" {
			typeFlag = "0"
		}
		archivePath, err := skills.NormalizeSyncRelativePath(readTarEntryPath(header), ArchivePathLabel)
		if err != nil {
			return err
		}
		targetPath, err := skills.ResolveSyncPathWithin(targetRoot, archivePath, ArchivePathLabel)
		if err != nil {
			return err
		}
		dataStart := offset
		dataEnd := dataStart + int(size)
		if dataEnd > len(data) {
			return fmt.Errorf("truncated plugin sync archive entry")
		}
		entryData := data[dataStart:dataEnd]
		offset = dataStart + tarPaddedBytes(int(size))

		switch typeFlag {
		case "5":
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
		case "0", "\x00":
			extractedBytes += size
			if options.MaxExtractedBytes > 0 && extractedBytes > options.MaxExtractedBytes {
				return &ArchiveExceedsLimitError{ActualBytes: extractedBytes, MaxBytes: options.MaxExtractedBytes}
			}
			fileMode := readTarMode(header)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(targetPath, entryData, fileMode); err != nil {
				return err
			}
			// writeFile applies umask on create; restore the archived mode
			// for plugin scripts' exec bit (the TS chmods on non-Windows).
			if runtimeOS() != "windows" {
				os.Chmod(targetPath, fileMode)
			}
		default:
			return fmt.Errorf("unsupported plugin archive entry type: %s", typeFlag)
		}
	}
	return nil
}

func readAllWithLimit(reader io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return io.ReadAll(reader)
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, &ArchiveExceedsLimitError{ActualBytes: int64(len(data)), MaxBytes: limit}
	}
	return data, nil
}

func appendTarEntry(buf *bytes.Buffer, sourcePath, archivePath string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		directoryEntryPath := archivePath
		if !strings.HasSuffix(directoryEntryPath, "/") {
			directoryEntryPath += "/"
		}
		writeTarHeader(buf, tarHeaderOptions{
			EntryPath:    directoryEntryPath,
			Mode:         info.Mode().Perm(),
			MtimeSeconds: info.ModTime().Unix(),
			Size:         0,
			TypeFlag:     "5",
		})
		children, err := os.ReadDir(sourcePath)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err := appendTarEntry(buf, filepath.Join(sourcePath, child.Name()),
				filepath.ToSlash(filepath.Join(archivePath, child.Name()))); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported plugin archive source: %s", sourcePath)
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	appendTarFile(buf, archivePath, content, info.Mode().Perm())
	return nil
}

func appendTarFile(buf *bytes.Buffer, archivePath string, content []byte, mode os.FileMode) {
	if mode == 0 {
		mode = 0o644
	}
	writeTarHeader(buf, tarHeaderOptions{
		EntryPath:    archivePath,
		Mode:         mode,
		MtimeSeconds: timeNowUnix(),
		Size:         int64(len(content)),
		TypeFlag:     "0",
	})
	buf.Write(content)
	if padding := tarPaddedBytes(len(content)) - len(content); padding > 0 {
		buf.Write(make([]byte, padding))
	}
}

type tarHeaderOptions struct {
	EntryPath    string
	Mode         os.FileMode
	MtimeSeconds int64
	Size         int64
	TypeFlag     string
}

func writeTarHeader(buf *bytes.Buffer, options tarHeaderOptions) {
	header := make([]byte, tarBlockSize)
	name, prefix := splitTarEntryPath(options.EntryPath)
	writeTarString(header, name, 0, 100)
	writeTarOctal(header, uint64(options.Mode.Perm()), 100, 8)
	writeTarOctal(header, 0, 108, 8)
	writeTarOctal(header, 0, 116, 8)
	writeTarOctal(header, uint64(options.Size), 124, 12)
	writeTarOctal(header, uint64(options.MtimeSeconds), 136, 12)
	for i := 148; i < 156; i++ {
		header[i] = 0x20
	}
	writeTarString(header, options.TypeFlag, 156, 1)
	writeTarString(header, "ustar", 257, 6)
	writeTarString(header, "00", 263, 2)
	writeTarString(header, "nex", 265, 32)
	writeTarString(header, "nex", 297, 32)
	writeTarString(header, prefix, 345, 155)
	var checksum int
	for _, b := range header {
		checksum += int(b)
	}
	encoded := fmt.Sprintf("%06o", checksum)
	writeTarString(header, encoded[len(encoded)-6:], 148, 6)
	header[154] = 0
	header[155] = 0x20
	buf.Write(header)
}

func splitTarEntryPath(entryPath string) (name, prefix string) {
	if len(entryPath) <= 100 {
		return entryPath, ""
	}
	segments := strings.Split(entryPath, "/")
	for index := len(segments) - 1; index > 0; index-- {
		prefix = strings.Join(segments[:index], "/")
		name = strings.Join(segments[index:], "/")
		if len(prefix) <= 155 && len(name) <= 100 {
			return name, prefix
		}
	}
	return "", ""
}

func readTarEntryPath(header []byte) string {
	name := readTarString(header, 0, 100)
	prefix := readTarString(header, 345, 345+155)
	if prefix != "" {
		return prefix + "/" + name
	}
	return name
}

func readTarString(buffer []byte, offset, end int) string {
	slice := buffer[offset:end]
	if index := bytes.IndexByte(slice, 0); index >= 0 {
		slice = slice[:index]
	}
	return string(slice)
}

func readTarOctal(buffer []byte, offset, length int) (int64, error) {
	raw := strings.TrimSpace(readTarString(buffer, offset, offset+length))
	if raw == "" {
		return 0, nil
	}
	var value int64
	if _, err := fmt.Sscanf(raw, "%o", &value); err != nil {
		return 0, err
	}
	return value, nil
}

func readTarMode(header []byte) os.FileMode {
	mode, err := readTarOctal(header, 100, 8)
	if err != nil || mode <= 0 {
		return 0o644
	}
	return os.FileMode(mode) & 0o777
}

func writeTarString(buffer []byte, value string, offset, length int) {
	source := []byte(value)
	if len(source) > length {
		panic("tar header value too long: " + value)
	}
	copy(buffer[offset:offset+len(source)], source)
}

func writeTarOctal(buffer []byte, value uint64, offset, length int) {
	encoded := fmt.Sprintf("%0*o", length-1, value)
	if len(encoded) > length-1 {
		encoded = encoded[len(encoded)-(length-1):]
	}
	writeTarString(buffer, encoded, offset, length-1)
	buffer[offset+length-1] = 0
}

func tarPaddedBytes(size int) int {
	return (size + tarBlockSize - 1) / tarBlockSize * tarBlockSize
}

func isAllZero(slice []byte) bool {
	for _, b := range slice {
		if b != 0 {
			return false
		}
	}
	return true
}

func marshalIndentedNL(value any) ([]byte, error) {
	data, err := jsonMarshalIndent(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
