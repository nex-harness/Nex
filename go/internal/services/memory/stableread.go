// stableread.go: the stable single-file read (projectMemoryStableRead.ts)
// — stat → O_NOFOLLOW open → re-validate → re-stat → bounded read → final
// stat; any identity change fails closed with the contract error codes.
package memory

import (
	"fmt"
	"os"
)

// fileIdentityError marks the changed/limit errors with their codes.
type fileIdentityError struct {
	code    string
	message string
}

func (e *fileIdentityError) Error() string { return e.message }

// Code exposes the contract error code.
func (e *fileIdentityError) Code() string { return e.code }

// readProjectMemoryFileStable ports readProjectMemoryFileFromStableHandle:
// stat → open O_NOFOLLOW → re-validate path → re-stat → read bounded →
// final stat; any identity change fails closed.
func readProjectMemoryFileStable(fileName, filePath string, validatePath func() error) (*ReadResult, error) {
	preOpenStat, err := lstatIdentity(filePath)
	if err != nil {
		return nil, err
	}
	if !preOpenStat.isFile || preOpenStat.isSymlink {
		return nil, fmt.Errorf("Project Memory file is not a regular file: %s", filePath)
	}
	// The path check is followed by a re-resolving read in the naive
	// implementation; the open handle (with O_NOFOLLOW where available)
	// keeps reading from the validated file even through a concurrent rename.
	handle, err := openNoFollow(filePath)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	openedStat, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if err := validatePath(); err != nil {
		return nil, err
	}
	postOpenStat, err := lstatIdentity(filePath)
	if err != nil {
		return nil, err
	}
	if !openedStat.Mode().IsRegular() ||
		!postOpenStat.isFile ||
		postOpenStat.isSymlink ||
		!sameFileSnapshot(statToIdentity(openedStat), preOpenStat) ||
		!sameFileSnapshot(statToIdentity(openedStat), postOpenStat) {
		return nil, &fileIdentityError{code: FileChangedErrorCode, message: fmt.Sprintf("Project Memory file changed during preview: %s", fileName)}
	}
	if openedStat.Size() > previewMaxBytes {
		return nil, &fileIdentityError{code: PreviewLimitExceededErrorCode, message: fmt.Sprintf("Project Memory file exceeds the 5 MiB preview limit: %s", fileName)}
	}
	buffer := make([]byte, previewMaxBytes+1)
	total := 0
	for total < len(buffer) {
		read, err := handle.Read(buffer[total:])
		if read > 0 {
			total += read
		}
		if err != nil {
			break
		}
	}
	content := buffer[:total]
	finalStat, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	// A failed atomic rename falls back to an in-place overwrite on the same
	// inode; file identity alone cannot detect that — re-check size too.
	if !sameFileSnapshot(statToIdentity(openedStat), statToIdentity(finalStat)) {
		return nil, &fileIdentityError{code: FileChangedErrorCode, message: fmt.Sprintf("Project Memory file changed during preview: %s", fileName)}
	}
	if len(content) > previewMaxBytes || finalStat.Size() > previewMaxBytes {
		return nil, &fileIdentityError{code: PreviewLimitExceededErrorCode, message: fmt.Sprintf("Project Memory file exceeds the 5 MiB preview limit: %s", fileName)}
	}
	return &ReadResult{
		Content:   string(content),
		UpdatedAt: mtimeMs(finalStat),
	}, nil
}

// fileIdentity is the (dev, ino, size, mtimeNs, ctimeNs) snapshot the
// stable read compares; statToIdentity is per-platform.
type fileIdentity struct {
	dev, ino, size    int64
	mtimeNs, ctimeNs  int64
	isFile, isSymlink bool
}

func lstatIdentity(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return statToIdentity(info), nil
}

func sameFileSnapshot(left, right fileIdentity) bool {
	return left.dev == right.dev &&
		left.ino == right.ino &&
		left.size == right.size &&
		left.mtimeNs == right.mtimeNs &&
		left.ctimeNs == right.ctimeNs
}

func mtimeMs(info os.FileInfo) float64 {
	return float64(info.ModTime().UnixNano()) / 1_000_000
}
