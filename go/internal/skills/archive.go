// archive.go: the skill-sync tar.gz archive codec, porting
// skillSyncArchive.ts. The format is a plain ustar tar (dir entries with
// trailing slash, `nex` as owner/group) gzipped with the stdlib — byte-shape
// compatible with the Node implementation (gzip bytes themselves are not
// pinned; tar contents and extraction semantics are).
//
// Security invariants enforced here (mirroring the TS):
//   - every entry name passes NormalizeSyncRelativePath (zip-slip, absolute
//     path, drive path, dot-dot segment rejection);
//   - extracted content is capped by MaxExtractedBytes (zip-bomb brake);
//   - only type 0 (\0) files and type 5 directories are accepted — symlinks,
//     devices, hardlinks are rejected outright (symlink-escape policy);
//   - truncated archives error rather than partially extract.
package skills

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	tarBlockSize = 512
	tarEndBytes  = tarBlockSize * 2
	// ArchivePathLabel is the error label for skill archive entry names.
	ArchivePathLabel = "unsafe skill archive path"
)

// ArchiveEntry pairs an on-disk source path with its archive-relative path.
type ArchiveEntry struct {
	SourcePath  string
	ArchivePath string
}

// SizeLimitError carries the structured skill-sync size-limit payload
// (createSkillSyncSizeLimitError). Code is the stable
// SKILL_SYNC_SIZE_LIMIT_ERROR_CODE the UI branches on.
type SizeLimitError struct {
	ActualBytes int64
	MaxBytes    int64
	Phase       string // "selected-content" | "archive" | "extracted-content"
}

// SKILLSyncSizeLimitErrorCode is the stable error code
// (SKILL_SYNC_SIZE_LIMIT_ERROR_CODE in @nex/shared).
const SKILLSyncSizeLimitErrorCode = "SKILL_SYNC_SIZE_LIMIT_EXCEEDED"

func (e *SizeLimitError) Error() string {
	return fmt.Sprintf("skill sync size limit exceeded: %d/%d (%s)", e.ActualBytes, e.MaxBytes, e.Phase)
}

// Code returns the contract error code.
func (e *SizeLimitError) Code() string { return SKILLSyncSizeLimitErrorCode }

// CreateSyncArchive tars the entries (recursively for directories, children
// sorted by name) and gzips the result.
func CreateSyncArchive(entries []ArchiveEntry) ([]byte, error) {
	var buf bytes.Buffer
	for _, entry := range entries {
		archivePath, err := NormalizeSyncRelativePath(entry.ArchivePath, ArchivePathLabel)
		if err != nil {
			return nil, err
		}
		if err := appendTarEntry(&buf, entry.SourcePath, archivePath); err != nil {
			return nil, err
		}
	}
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
	// MaxExtractedBytes limits the sum of file entry sizes (0 = unlimited).
	MaxExtractedBytes int64
}

// ExtractSyncArchive extracts the archive under targetDir with the security
// invariants documented on the package.
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
		return fmt.Errorf("invalid skill sync archive: %w", err)
	}
	defer gzipReader.Close()

	// Bound the decompressed stream: the tar payload cannot exceed
	// maxExtracted + end blocks + entry headers even in the worst case, so a
	// gzip bomb fails the limit read instead of ballooning memory.
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
	sawEndBlock := false
	for offset+tarBlockSize <= len(data) {
		header := data[offset : offset+tarBlockSize]
		offset += tarBlockSize
		if isAllZero(header) {
			sawEndBlock = true
			break
		}

		size, err := readTarOctal(header, 124, 12)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid skill sync archive entry size")
		}
		typeFlag := readTarString(header, 156, 157)
		if typeFlag == "" {
			typeFlag = "0"
		}
		archivePath, err := NormalizeSyncRelativePath(readTarEntryPath(header), ArchivePathLabel)
		if err != nil {
			return err
		}
		targetPath, err := ResolveSyncPathWithin(targetRoot, archivePath, ArchivePathLabel)
		if err != nil {
			return err
		}

		dataStart := offset
		dataEnd := dataStart + int(size)
		if dataEnd > len(data) {
			return fmt.Errorf("truncated skill sync archive entry")
		}
		entryData := data[dataStart:dataEnd]
		offset = dataStart + tarPaddedBytes(int(size))

		switch typeFlag {
		case "5":
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
		case "0", "\x00":
			nextExtracted := extractedBytes + size
			if options.MaxExtractedBytes > 0 && nextExtracted > options.MaxExtractedBytes {
				return &SizeLimitError{
					ActualBytes: nextExtracted,
					MaxBytes:    options.MaxExtractedBytes,
					Phase:       "extracted-content",
				}
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(targetPath, entryData, 0o644); err != nil {
				return err
			}
			extractedBytes = nextExtracted
		default:
			// Symlinks (type 2), hardlinks (1), devices (3/4) and anything
			// else are refused: extraction must not be able to plant a link
			// that escapes the target root.
			return fmt.Errorf("unsupported skill archive entry type: %s", typeFlag)
		}
	}
	// A well-formed archive ends with the two zero end blocks. When the
	// loop exits without seeing them, the stream was cut short mid-entry or
	// mid-padding — the TS reader errors on the short read; error here too
	// unless the remainder is exactly zero padding.
	if sawEndBlock {
		return nil
	}
	remainder := len(data) - offset
	if remainder > 0 && !isAllZero(data[offset:]) {
		return fmt.Errorf("truncated skill sync archive")
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
		return nil, &SizeLimitError{
			ActualBytes: int64(len(data)),
			MaxBytes:    limit,
			Phase:       "extracted-content",
		}
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
			Mode:         info.Mode(),
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
		return fmt.Errorf("unsupported skill archive source: %s", sourcePath)
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	writeTarHeader(buf, tarHeaderOptions{
		EntryPath:    archivePath,
		Mode:         info.Mode(),
		MtimeSeconds: info.ModTime().Unix(),
		Size:         int64(len(content)),
		TypeFlag:     "0",
	})
	buf.Write(content)
	if padding := tarPaddedBytes(len(content)) - len(content); padding > 0 {
		buf.Write(make([]byte, padding))
	}
	return nil
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

// SplitTarEntryPathError is returned by CreateSyncArchive when a path is too
// long for ustar (splitTarEntryPath throws).
type SplitTarEntryPathError struct{ Path string }

func (e *SplitTarEntryPathError) Error() string {
	return fmt.Sprintf("skill archive path is too long: %s", e.Path)
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

// ensureSplitTarEntryPath wraps splitTarEntryPath with the TS error.
func ensureSplitTarEntryPath(entryPath string) (string, string, error) {
	name, prefix := splitTarEntryPath(entryPath)
	if len(entryPath) > 100 && name == "" && prefix == "" {
		return "", "", &SplitTarEntryPathError{Path: entryPath}
	}
	return name, prefix, nil
}

// TimeNow is the clock seam for archive headers (tests pin mtime).
var TimeNow = time.Now
