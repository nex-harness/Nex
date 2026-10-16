// client.go is the per-connection Client view of a managed agent process
// (step 3.10.1): send/stdout/alive/dispose plus the identity accessors.
// Split out of manager.go to stay under the 400-line archcheck limit.
package agentproc

type Client struct {
	proc *child
	// generation is the epoch this handle was created for.
	generation int
	// identity is the runtime identity of the process behind this handle.
	identity RuntimeIdentity
	// workspace is the identity triple the process was spawned for.
	workspace WorkspaceRef
}

// PID is the agent process id (its process-group id on Unix).
func (c *Client) PID() int {
	if c == nil || c.proc == nil {
		return 0
	}
	return c.proc.PID()
}

// Generation is the epoch of the process behind this handle.
func (c *Client) Generation() int {
	if c == nil {
		return 0
	}
	return c.generation
}

// Identity is the runtime identity of the process behind this handle.
func (c *Client) Identity() RuntimeIdentity {
	if c == nil {
		return RuntimeIdentity{}
	}
	return c.identity
}

// Workspace is the identity triple the process was spawned for.
func (c *Client) Workspace() WorkspaceRef {
	if c == nil {
		return WorkspaceRef{}
	}
	return c.workspace
}

// StdoutLines delivers every complete line the agent writes to stdout while it is
// still running — the frame stream the protocol client (3.10.2) reads. Lines are
// dropped rather than blocking the agent if the reader falls behind.
func (c *Client) StdoutLines() <-chan string {
	if c == nil || c.proc == nil {
		return nil
	}
	return c.proc.stdoutLines
}

// Send writes raw bytes to the agent's stdin. The protocol client frames on top.
func (c *Client) Send(data []byte) error {
	if c == nil || c.proc == nil {
		return ErrTransportClosed
	}
	return c.proc.Write(data)
}

// IsAlive reports whether the process behind this handle is still running.
func (c *Client) IsAlive() bool {
	return c != nil && c.proc != nil && c.proc.IsAlive()
}

// Exited returns a channel closed when the process exits, for callers that need to
// wait without owning the reap.
func (c *Client) Exited() <-chan struct{} { return c.proc.exitedSignal() }

// ExitInfo returns the observed terminal state once the process has exited.
//
// It waits on the broadcast exit signal and then reads the published state under
// the child's lock, rather than receiving from child.exited: that channel carries
// the value but nothing ever sends on it (the exit observer publishes into the
// locked fields and closes the broadcast signal), so receiving from it blocked
// forever. Fixing that here rather than at the call site keeps every future
// consumer on the working path.
func (c *Client) ExitInfo() ExitInfo {
	if c == nil || c.proc == nil {
		return ExitInfo{}
	}
	return c.proc.WaitExit()
}

// Dispose requests the graceful close (stdin EOF, bounded wait, then tree kill).
func (c *Client) Dispose() {
	if c == nil || c.proc == nil {
		return
	}
	c.proc.CloseStdin()
}

// newClient wraps a freshly spawned child with the identity the manager assigned.
func newClient(proc *child, identity RuntimeIdentity, ws WorkspaceRef) *Client {
	return &Client{proc: proc, generation: identity.Generation, identity: identity, workspace: ws}
}

// GetClient returns the resident client for a workspace, spawning one on first
