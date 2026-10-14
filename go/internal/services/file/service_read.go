package file

// service_read.go holds the read/preview half of the IFileService port:
// readTextFile, readFileRange, readMediaPreview, readBinaryPreview — the
// byte clamps and error strings are part of the RPC contract.

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
)

// ReadTextFile ports readTextFile with the 256KB cap and binary detection.
func (s *Service) ReadTextFile(ctx context.Context, path string, offset, length *int64) (TextSlice, error) {
	info, err := os.Stat(path)
	if err != nil {
		return TextSlice{}, err
	}
	if info.IsDir() {
		return TextSlice{}, fmt.Errorf("Path is not a file: %s", path)
	}
	off := int64(0)
	if offset != nil && *offset > 0 {
		off = *offset
	}
	size := info.Size()
	if off >= size {
		return TextSlice{Path: path, Content: "", Offset: off, BytesRead: 0, TotalBytes: size, Truncated: false, IsBinary: false}, nil
	}
	target := clampReadLength(length)
	remaining := size - off
	readLen := target
	if remaining < readLen {
		readLen = remaining
	}
	buf := make([]byte, readLen)
	f, err := os.Open(path)
	if err != nil {
		return TextSlice{}, err
	}
	defer f.Close()
	n, err := f.ReadAt(buf, off)
	if err != nil && n == 0 {
		return TextSlice{}, err
	}
	buf = buf[:n]
	isBinary := isProbablyBinary(buf)
	content := ""
	if !isBinary {
		content = string(buf)
	}
	return TextSlice{
		Path:       path,
		Content:    content,
		Offset:     off,
		BytesRead:  int64(n),
		TotalBytes: size,
		Truncated:  off+int64(n) < size,
		IsBinary:   isBinary,
	}, nil
}

// ReadFileRange ports readFileRange (top-level bytes for RPC binary channel).
func (s *Service) ReadFileRange(ctx context.Context, path string, offset, length int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("Path is not a file: %s", path)
	}
	off := offset
	if off < 0 {
		off = 0
	}
	size := info.Size()
	if off >= size {
		return []byte{}, nil
	}
	readLen := clampBinaryReadLength(length)
	if size-off < readLen {
		readLen = size - off
	}
	buf := make([]byte, readLen)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := f.ReadAt(buf, off)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// ReadMediaPreview ports readMediaPreview.
func (s *Service) ReadMediaPreview(ctx context.Context, path string, maxBytes *int64) (MediaPreview, error) {
	info, err := os.Stat(path)
	if err != nil {
		return MediaPreview{}, err
	}
	if info.IsDir() {
		return MediaPreview{}, fmt.Errorf("Path is not a file: %s", path)
	}
	limit := clampMediaPreviewBytes(maxBytes)
	if info.Size() > limit {
		return MediaPreview{}, fmt.Errorf("File is too large to preview: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return MediaPreview{}, err
	}
	return MediaPreview{
		Path:       path,
		MediaType:  inferMediaType(path),
		DataBase64: base64.StdEncoding.EncodeToString(content),
		TotalBytes: info.Size(),
	}, nil
}

// ReadBinaryPreview ports readBinaryPreview (Office ZIP/OLE payloads, 25MB).
func (s *Service) ReadBinaryPreview(ctx context.Context, path string, maxBytes *int64) (BinaryPreview, error) {
	info, err := os.Stat(path)
	if err != nil {
		return BinaryPreview{}, err
	}
	if info.IsDir() {
		return BinaryPreview{}, fmt.Errorf("Path is not a file: %s", path)
	}
	limit := clampBinaryPreviewBytes(maxBytes)
	if info.Size() > limit {
		return BinaryPreview{}, fmt.Errorf("File is too large to preview: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return BinaryPreview{}, err
	}
	return BinaryPreview{
		Path:       path,
		DataBase64: base64.StdEncoding.EncodeToString(content),
		TotalBytes: info.Size(),
	}, nil
}
