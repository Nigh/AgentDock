// Package session manages tmux-like PTY sessions on the local PC.
// Sessions survive browser disconnects; they live as long as this
// process (and their shell) does.
package session

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"agentdock/internal/protocol"
)

// scrollbackSize is replayed on attach. ponytail: 256KB per session is
// plenty for a screen restore; a real scrollback store would persist to disk.
const scrollbackSize = 256 * 1024

type Session struct {
	protocol.Session
	ptmx *os.File
	cmd  *exec.Cmd

	mu   sync.Mutex
	ring *ring
}

type Manager struct {
	log *slog.Logger

	mu       sync.Mutex
	sessions map[string]*Session

	// OnOutput / OnExit are invoked from per-session reader goroutines.
	OnOutput func(sessionID string, data []byte)
	OnExit   func(sessionID string)
}

func NewManager(log *slog.Logger) *Manager {
	return &Manager{log: log, sessions: map[string]*Session{}}
}

func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/bash"
}

func (m *Manager) Create(id, name, cwd, shell string) (*Session, error) {
	if shell == "" {
		shell = defaultShell()
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("directory does not exist: %s", cwd)
	}

	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "AGENTDOCK_SESSION="+name)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 32})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	now := time.Now().UTC()
	s := &Session{
		Session: protocol.Session{
			ID: id, Name: name, Cwd: cwd, Shell: shell,
			Pid: cmd.Process.Pid, CreatedAt: now, LastActive: now,
		},
		ptmx: ptmx,
		cmd:  cmd,
		ring: newRing(scrollbackSize),
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go m.readLoop(s)
	m.log.Info("session created", "id", id, "name", name, "pid", s.Pid, "cwd", cwd, "shell", shell)
	return s, nil
}

func (m *Manager) readLoop(s *Session) {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.mu.Lock()
			s.ring.Write(data)
			s.LastActive = time.Now().UTC()
			s.mu.Unlock()
			if m.OnOutput != nil {
				m.OnOutput(s.ID, data)
			}
		}
		if err != nil { // EOF/EIO: the shell exited
			break
		}
	}
	s.cmd.Wait()
	s.ptmx.Close()
	m.mu.Lock()
	delete(m.sessions, s.ID)
	m.mu.Unlock()
	m.log.Info("session exited", "id", s.ID, "name", s.Name)
	if m.OnExit != nil {
		m.OnExit(s.ID)
	}
}

func (m *Manager) get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *Manager) Write(id string, data []byte) error {
	s := m.get(id)
	if s == nil {
		return fmt.Errorf("no such session: %s", id)
	}
	s.mu.Lock()
	s.LastActive = time.Now().UTC()
	s.mu.Unlock()
	_, err := s.ptmx.Write(data)
	return err
}

func (m *Manager) Resize(id string, cols, rows uint16) error {
	s := m.get(id)
	if s == nil {
		return fmt.Errorf("no such session: %s", id)
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Buffer returns the scrollback snapshot for attach replay.
func (m *Manager) Buffer(id string) ([]byte, error) {
	s := m.get(id)
	if s == nil {
		return nil, fmt.Errorf("no such session: %s", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.Bytes(), nil
}

// LiveCwd returns the session shell's current working directory.
// ponytail: Linux-only /proc readlink of the shell pid (not the
// foreground child); falls back to the spawn cwd where /proc is absent.
func (m *Manager) LiveCwd(id string) (string, error) {
	s := m.get(id)
	if s == nil {
		return "", fmt.Errorf("no such session: %s", id)
	}
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", s.Pid)); err == nil {
		return cwd, nil
	}
	return s.Cwd, nil
}

func (m *Manager) Kill(id string) error {
	s := m.get(id)
	if s == nil {
		return fmt.Errorf("no such session: %s", id)
	}
	// Kill the whole process group so agent children die too.
	if pgid, err := syscall.Getpgid(s.Pid); err == nil {
		syscall.Kill(-pgid, syscall.SIGHUP)
	} else {
		s.cmd.Process.Kill()
	}
	return nil
}

func (m *Manager) List() []protocol.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		out = append(out, s.Session)
		s.mu.Unlock()
	}
	return out
}

// Shutdown kills all sessions (called on graceful exit).
func (m *Manager) Shutdown() {
	for _, s := range m.List() {
		m.Kill(s.ID)
	}
}
