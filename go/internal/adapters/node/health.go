// health.go is readiness: how the Go front decides that the Node upstream is up,
// and how it notices that it stopped being up.
//
// Two signals, for two different questions:
//
//   - Port discovery. createHttpServer logs `http://<host>:<port>` from its listen
//     callback and nothing else — packages/server/src/http.ts has no IPC channel and
//     no machine-readable ready message (the CLI's forked Core does, via the
//     `ready` core message; the standalone HTTP entry does not). With PORT=0 the
//     real port is therefore only observable in that log line, so it is parsed out
//     of the child's stdout. That is a string format, not a protocol, which is why
//     the regex is anchored on the whole line and why a line that does not match is
//     simply ignored rather than treated as an error.
//
//   - Liveness. `GET /api/auth/session` is the endpoint the plan names and the one
//     the Docker acceptance list uses (docker-single-image.md). It answers 200 with
//     {"authRequired":…,"authenticated":…} whether or not a token is configured, and
//     it is NOT token protected (isTokenProtectedPath returns false for /api/auth/*),
//     so probing it does not consume a session, mint a cookie, or exercise the login
//     rate limiter. `?token=` is still sent when a secret exists, because that is how
//     the middleware sees a programmatic client, and the 401-vs-200 difference is
//     itself the signal that auth is up.
package node

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/exec"
)

// HealthPath is GET /api/auth/session, from webAuth.ts mountRoutes.
const HealthPath = "/api/auth/session"

// Defaults for the health loop. A probe is cheap, so the interval is short; the
// timeout bounds one attempt so a half-open TCP connection cannot hold the loop.
const (
	DefaultHealthInterval = 1 * time.Second
	DefaultHealthTimeout  = 2 * time.Second
	// DefaultReadyTimeout is the budget for the FIRST successful probe after a spawn.
	// The TypeScript supervisor uses coreReadyTimeoutMs ?? 15_000 for the equivalent
	// wait; a Node process that has not answered in that long is not coming.
	DefaultReadyTimeout = 15 * time.Second
	// maxHealthBody bounds the probe response. A server that streams more than this
	// is not the server we are probing, and reading it would be the wrong way out.
	maxHealthBody = 64 << 10
)

// listenLine matches the `http://127.0.0.1:41234` line createHttpServer's listen
// callback logs. The port group is required; the host group tolerates IPv6 brackets
// so a deployment that binds ::1 is still discoverable.
var listenLine = regexp.MustCompile(`http://(\[[0-9A-Fa-f:.]+\]|[0-9A-Za-z_.\-]+):(\d{1,5})\s*$`)

// ProbePort extracts the listen port from one line of the child's stdout.
//
// It returns 0 when the line is not the listen announcement: the child logs its
// startup banner, warnings and errors too, and none of them mean anything about
// the port.
func ProbePort(line string) int {
	matches := listenLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if matches == nil {
		return 0
	}
	port, err := strconv.Atoi(matches[2])
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// ScanForPort reads r until a listen line appears, ctx is done, or the reader
// ends.
//
// The supervisor does not use this: it installs the line tap on the child's stdout
// collector (exec.Options.StdoutLines) and selects on the channel instead, so no
// second reader competes for the same pipe. This helper exists for a caller that has
// a captured log file or any other reader, and it is the exact parsing the supervisor
// relies on.
func ScanForPort(ctx context.Context, r io.Reader) (int, error) {
	if r == nil {
		return 0, errors.New("node: no output stream to scan for the listen port")
	}
	type result struct {
		port int
		err  error
	}
	found := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 8<<10), 1<<20)
		for scanner.Scan() {
			if port := ProbePort(scanner.Text()); port > 0 {
				found <- result{port: port}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			found <- result{err: fmt.Errorf("node: read upstream output: %w", err)}
			return
		}
		found <- result{err: errors.New("node: upstream closed its output before announcing a port")}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case got := <-found:
		return got.port, got.err
	}
}

// HealthChecker probes one upstream.
//
// Token is the shared secret generated for the CURRENT generation. It is a field
// rather than a constructor argument because the secret rotates on every restart,
// and a checker holding a stale token would keep getting 401 and would report a
// perfectly healthy process as down.
type HealthChecker struct {
	// BaseURL is the upstream origin, e.g. "http://127.0.0.1:41234".
	BaseURL string
	// Token is the current generation's secret. Empty means the child runs without
	// auth, which is only reachable when a caller deliberately configured it.
	Token string
	// Timeout bounds one attempt. Zero selects DefaultHealthTimeout.
	Timeout time.Duration
	// Interval is the loop's period. Zero selects DefaultHealthInterval.
	Interval time.Duration
	// HTTPClient issues the probes. Nil uses a client with Timeout only.
	HTTPClient *http.Client
}

// NewHealthChecker returns a checker for baseURL with the defaults applied.
func NewHealthChecker(baseURL, token string) *HealthChecker {
	return &HealthChecker{BaseURL: strings.TrimRight(baseURL, "/"), Token: token}
}

// Ready is one probe result.
type Ready struct {
	// OK is true when the endpoint answered. Both 200 and 401 count: a 401 means the
	// middleware is up and rejecting, which is a running server — the difference is
	// only whether the probe carried the right secret.
	OK bool
	// StatusCode is the HTTP status, or 0 when the probe did not reach the server.
	StatusCode int
	// Err is the transport-level failure, if any.
	Err error
}

// Check performs one probe.
//
// A 5xx is NOT OK: the process is listening but broken, and reporting "ready" there
// would let the gateway open upstream legs that immediately fail. A connection error
// is not OK either — that is the normal state while the child is still booting, so
// callers retry rather than treat it as fatal.
func (c *HealthChecker) Check(ctx context.Context) Ready {
	if strings.TrimSpace(c.BaseURL) == "" {
		return Ready{Err: errors.New("node: health check needs an upstream base URL")}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := c.BaseURL + HealthPath
	if c.Token != "" {
		// ?token= is webAuth's programmatic-client path (webAuth.ts reads url.searchParams
		// before the session lookup). Sending it makes a 200 mean "this is MY upstream"
		// rather than "some server is on this port".
		target += "?token=" + url.QueryEscape(c.Token)
	}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target, nil)
	if err != nil {
		return Ready{Err: err}
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	response, err := client.Do(request)
	if err != nil {
		return Ready{Err: err}
	}
	defer response.Body.Close()
	// Drain a bounded amount so the connection can be reused; the body is not read.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxHealthBody))
	if response.StatusCode >= 500 {
		return Ready{StatusCode: response.StatusCode, Err: fmt.Errorf("node: upstream health returned %d", response.StatusCode)}
	}
	return Ready{OK: true, StatusCode: response.StatusCode}
}

// WaitReady probes until the endpoint answers or the deadline passes.
//
// poll, when non-nil, is the sleep between attempts. Tests pass a function that
// advances a fake clock instead of really sleeping, which is what makes the
// crash-budget timing testable without spending sixteen real seconds.
func (c *HealthChecker) WaitReady(ctx context.Context, timeout time.Duration, poll func(ctx context.Context, d time.Duration) error) error {
	if timeout <= 0 {
		timeout = DefaultReadyTimeout
	}
	if poll == nil {
		poll = sleepCtx
	}
	deadline := time.Now().Add(timeout)
	var last Ready
	for {
		last = c.Check(ctx)
		if last.OK {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("node: upstream did not become ready within %s: %w", timeout, last.failure())
		}
		if err := poll(ctx, c.Interval); err != nil {
			return err
		}
	}
}

// scanPort waits for the listen line on the child's stdout tap.
//
// It races the tap against the child's exit: a process that died during startup never
// prints the line, and waiting for the timeout instead of noticing the exit would
// turn a fast, obvious failure into a fifteen-second stall on every crash-loop
// iteration. The exit signal doubles as the error text, because a child that exited
// before listening has usually already explained itself on stderr.
func scanPort(ctx context.Context, lines <-chan string, proc *exec.Process) (int, error) {
	exited := make(chan struct{})
	go func() {
		proc.Wait()
		close(exited)
	}()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-exited:
			// Drain anything the tap already holds: the line and the exit can be
			// observed in either order, and losing the line to the race would restart a
			// child that had in fact started.
			for {
				select {
				case line := <-lines:
					if port := ProbePort(line); port > 0 {
						return port, nil
					}
					continue
				default:
				}
				break
			}
			return 0, fmt.Errorf("upstream pid %d exited before announcing a listen port: %s",
				proc.PID(), strings.TrimSpace(collectedTail(proc)))
		case line := <-lines:
			if port := ProbePort(line); port > 0 {
				return port, nil
			}
		}
	}
}

// collectedTail is the child's retained stderr, for a failure message. It is
// truncated: the point is "why did it die", not a transcript.
func collectedTail(proc *exec.Process) string {
	tail := proc.Stderr().Result().Tail
	if len(tail) > 512 {
		tail = tail[len(tail)-512:]
	}
	return tail
}

func (r Ready) failure() error {
	if r.Err != nil {
		return r.Err
	}
	return fmt.Errorf("last health status %d", r.StatusCode)
}

// WatchHealth polls on the interval and calls onChange when readiness flips.
//
// It returns when ctx is done. A flapping upstream (ready, not ready, ready) is
// reported as two transitions, which is what the gateway needs: the legs it opened
// against a dead upstream must be closed, and the ones opened against a live one
// must not be reused after it died.
//
// The loop never retries inside one iteration: onChange is expected to be quick
// (close legs, mark the address unusable), and a slow consumer must not turn into a
// probe backlog.
func (c *HealthChecker) WatchHealth(ctx context.Context, onChange func(Ready)) error {
	interval := c.Interval
	if interval <= 0 {
		interval = DefaultHealthInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	previous := Ready{}
	first := true
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		current := c.Check(ctx)
		if first || current.OK != previous.OK {
			first = false
			previous = current
			if onChange != nil {
				onChange(current)
			}
		}
	}
}

// sleepCtx waits for d, or until ctx is done. A cancel during the wait must abort
// the health loop rather than run one more probe.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// PortOpen reports whether something is listening on host:port. It is a last-resort
// discovery path for a child that was already running (an operator attaching to an
// existing upstream) and must not be used as the readiness signal: an open port says
// nothing about whether the server can serve a request.
func PortOpen(host string, port int) bool {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	connection, err := net.DialTimeout("tcp", address, DefaultHealthTimeout)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}
