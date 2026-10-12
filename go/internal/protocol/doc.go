// Package protocol implements the Nex Protocol transport used between the
// host and the agent: LF-delimited JSON messages over stdio (frame.go,
// envelope.go) and the v4 topic wire layer (wire*.go).
//
// The package does not import os/exec, net/http or database/sql; byte streams
// are passed in as io.Reader / io.Writer so the layer stays testable.
package protocol
