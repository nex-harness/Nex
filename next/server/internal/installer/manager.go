// Manager owns the install sessions: dedup by target (decision 8), the
// bounded session store (10-minute retention), and the async runner.
package installer

import (
	"log/slog"
	"sync"
	"time"
)

// RegistryLookup reports whether a runner id is registered (the wait-for-hello
// closure calls this).
type RegistryLookup func(runnerID string) bool

// Manager runs install sessions.
type Manager struct {
	Logger  *slog.Logger
	Bundles Bundles
	// LookupRunner is the server's registry lookup (is this runner id
	// connected).
	LookupRunner RegistryLookup
	// ServerURL is what the installed runner dials (wss://host/runner).
	ServerURL string
	// RunnerToken is the bearer token installed into the runner env.
	RunnerToken string

	mu       sync.Mutex
	sessions map[string]*Session
	// inflight dedups by "username@host" (decision 8).
	inflight map[string]string
	nextID   int64
}

// NewManager builds the manager.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		Logger:   logger,
		sessions: map[string]*Session{},
		inflight: map[string]string{},
	}
}

// Start kicks off (or joins) one install. The returned session is always
// populated; `started` distinguishes "created" from "joined an existing run".
func (m *Manager) Start(target SSHTarget) (session *Session, started bool) {
	key := target.String()
	m.mu.Lock()
	if id, ok := m.inflight[key]; ok {
		if existing := m.sessions[id]; existing != nil && existing.Status == StatusRunning {
			m.mu.Unlock()
			return existing, false
		}
	}
	m.nextID++
	id := time.Now().UTC().Format("20060102T150405") + "-" + itoa(m.nextID)
	session = &Session{
		ID:        id,
		Target:    key,
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	}
	m.sessions[id] = session
	m.inflight[key] = id
	m.mu.Unlock()

	runnerID := generateRunnerID()
	session.RunnerID = runnerID
	request := installRequest{
		Target:    target,
		RunnerID:  runnerID,
		RunnerTok: m.RunnerToken,
		ServerURL: m.ServerURL,
		Name:      target.Name,
	}

	go m.run(session, request, key)
	return session, true
}

func (m *Manager) run(session *Session, request installRequest, targetKey string) {
	defer func() {
		m.mu.Lock()
		delete(m.inflight, targetKey)
		m.mu.Unlock()
	}()
	wait := WaitForRunner(m.LookupRunner)
	err := session.install(request, m.Bundles, wait)
	if err != nil {
		session.Finish(StatusFailed, err.Error())
		m.Logger.Warn("runner install failed", "target", targetKey, "error", err.Error())
		go m.expire(session)
		return
	}
	session.Finish(StatusSucceeded, "")
	m.Logger.Info("runner install succeeded", "target", targetKey, "runnerId", session.RunnerID)
	go m.expire(session)
}

// expire removes a completed session after 10 minutes (decision 8).
func (m *Manager) expire(session *Session) {
	time.Sleep(10 * time.Minute)
	m.mu.Lock()
	delete(m.sessions, session.ID)
	m.mu.Unlock()
}

// Get returns one session.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	return session, ok
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
