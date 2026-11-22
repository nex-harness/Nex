//go:build ios || android

package remote

import (
	"context"
	"errors"
	"net/http"
)

// ProbeTSNetSidecar reports that mobile sandboxes forbid downloading & executing external sidecars.
func ProbeTSNetSidecar(_ string) TSNetSidecarStatus {
	return TSNetSidecarStatus{
		PlatformSupported: false,
		Installed:         false,
		State:             "unsupported_mobile",
	}
}

// InstallTSNetSidecar is unsupported on iOS and Android due to OS binary execution sandboxing.
func InstallTSNetSidecar(_ context.Context, _, _ string, _ *http.Client) (TSNetSidecarStatus, error) {
	return ProbeTSNetSidecar(""), errors.ErrUnsupported
}

// UninstallTSNetSidecar is unsupported on iOS and Android.
func UninstallTSNetSidecar(_ string) error {
	return errors.ErrUnsupported
}

// TSNetHostOptions configures a managed nex-tsnet host subprocess on desktop/server platforms.
type TSNetHostOptions struct {
	DataDir     string
	Hostname    string
	ControlURL  string
	UpstreamURL string
	ListenPort  int
}

// TSNetHostRunner is a no-op stub on mobile platforms.
type TSNetHostRunner struct{}

// NewTSNetHostRunner constructs a no-op TSNetHostRunner on mobile platforms.
func NewTSNetHostRunner(_ string) *TSNetHostRunner { return &TSNetHostRunner{} }

// Start returns ErrUnsupported on mobile platforms.
func (*TSNetHostRunner) Start(_ TSNetHostOptions) error { return errors.ErrUnsupported }

// Status reports unsupported_mobile on mobile platforms.
func (*TSNetHostRunner) Status() TSNetSidecarStatus { return ProbeTSNetSidecar("") }

// Stop is a no-op on mobile platforms.
func (*TSNetHostRunner) Stop() {}
