//go:build ios || android

package app

import (
	"context"
	"errors"
)

const supportsLocalMode = false

func (h *HostService) startLocalBackendLocked(_ context.Context) (string, error) {
	return "", errors.ErrUnsupported
}

func (h *HostService) stopLocalBackendLocked() {}
