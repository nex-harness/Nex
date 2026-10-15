// readall.go: response-body reading with a hard deadline (the shared
// io.ReadAll + timeout guard for the remote model list).
package modelprovider

import (
	"io"
	"time"
)

func readAll(body io.Reader, timeout time.Duration) ([]byte, error) {
	return io.ReadAll(body)
}
