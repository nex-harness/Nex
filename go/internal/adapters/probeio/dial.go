// Package probeio is the I/O adapter for the ISystemService port (step 3.2):
// the TCP dial, the HTTP GET, and the `ssh -G` subprocess the intranet probe
// and ssh-config alias resolution need. internal/services/system stays free
// of net / net/http / os/exec (arch rule: external I/O lives in adapters).
package probeio

import (
	"context"
	"fmt"
	"net"
	"time"
)

// DialTCP connects to host:port with a timeout and returns the latency in
// milliseconds. It is the Go shape of the TS probeTcpPort.
func DialTCP(ctx context.Context, host string, port, timeoutMs int64) (int64, error) {
	started := time.Now()
	dialer := net.Dialer{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		return 0, err
	}
	conn.Close()
	latency := time.Since(started).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	return latency, nil
}
