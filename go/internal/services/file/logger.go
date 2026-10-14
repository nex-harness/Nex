package file

// logger.go is the minimal logging surface the file package needs — the port
// of the `Pick<ServiceLogger, "info" | "warn">` type workspaceFileIgnore.ts
// takes. The real serviceLogger adapter (adapters/logx) implements it; nil
// loggers are tolerated because the TS callers pass undefined in tests.

// serviceLogger is the info/warn subset of createServiceLogger's interface.
type serviceLogger interface {
	info(msg string)
	warn(msg string, err error)
}

// noopLogger drops everything.
type noopLogger struct{}

func (noopLogger) info(string)        {}
func (noopLogger) warn(string, error) {}
