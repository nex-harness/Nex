// Package proto is the host side of the Nex agent protocol (step 3.10.2): the
// request/response client, the startup gate, the agent→host interaction routing
// and the v4 wire-frame assembler.
//
// It sits on top of internal/protocol (message envelope, LF framing, the wire
// codec) and internal/agentproc (spawn + transport), which own the bytes. What
// lives here is everything that needs correlation state: a request id, a waiter,
// a timer, or a partially reassembled logical frame.
//
// Nothing here spawns a process or touches the filesystem; the caller owns the
// agentproc.Manager and passes in the frame stream and the write function. That
// keeps this package testable against a fake agent and against the real binary
// without either path being a special case.
package proto
