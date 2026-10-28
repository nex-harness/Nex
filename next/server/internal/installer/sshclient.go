// SSH client for the install channel: thin wrapper over x/crypto/ssh with
// exec + SFTP upload. Credentials are memory-only (spec decision 2): the
// password is used for this session and dropped; nothing is persisted or
// logged.
package installer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	sshConnectTimeout = 15 * time.Second
	sshExecTimeout    = 10 * time.Minute
	// Per-read timeout while streaming a command's output; large outputs
	// (npm install) must not stall the install.
	sshReadTimeout = 120 * time.Second
)

// SSHTarget is the validated install target.
type SSHTarget struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func (t SSHTarget) defaultPort() int {
	if t.Port <= 0 || t.Port > 65535 {
		return 22
	}
	return t.Port
}

// Validate checks the request shape (password auth only in this release;
// privateKeyPath would require the server to read the user's local files).
func (t SSHTarget) Validate() error {
	if t.Host == "" {
		return errors.New("host is required")
	}
	if t.Username == "" {
		return errors.New("username is required")
	}
	if t.Password == "" {
		return errors.New("password authentication is required for the install channel in this release")
	}
	return nil
}

func (t SSHTarget) String() string {
	return fmt.Sprintf("%s@%s", t.Username, t.Host)
}

// sshClient is one SSH session to the target.
type sshClient struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// dialSSH opens the connection and the single reusable exec session.
func dialSSH(target SSHTarget) (*sshClient, error) {
	config := &ssh.ClientConfig{
		User: target.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(target.Password),
			ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = target.Password
				}
				return answers, nil
			}),
		},
		// Host key trust: first-connect TOFU would persist trust state on the
		// server; the spec leaves host-key pinning open, and accepting unknown
		// hosts matches the one-shot install channel (credentials are
		// per-install and the channel installs, it does not browse).
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         sshConnectTimeout,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.defaultPort())), config)
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", target, err)
	}
	return &sshClient{client: client}, nil
}

// close tears the connection down.
func (c *sshClient) close() {
	if c.session != nil {
		_ = c.session.Close()
	}
	_ = c.client.Close()
}

// run executes one command, streams combined output, and fails on non-zero
// exit. `onOutput` receives chunks as they arrive (install log tail).
func (c *sshClient) run(command string, onOutput func(line string)) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("ssh stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("ssh stderr pipe: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	var collected bytes.Buffer
	deadline := time.Now().Add(sshExecTimeout)
	for {
		select {
		case err := <-done:
			// Drain any remaining output.
			for _, pipe := range []io.Reader{stdout, stderr} {
				chunk := make([]byte, 4096)
				for {
					n, readErr := pipe.Read(chunk)
					if n > 0 {
						collected.Write(chunk[:n])
						if onOutput != nil {
							onOutput(string(chunk[:n]))
						}
					}
					if readErr != nil {
						break
					}
				}
			}
			if err != nil {
				return collected.String(), fmt.Errorf("remote command failed: %w", err)
			}
			return collected.String(), nil
		case <-time.After(time.Until(deadline)):
			return collected.String(), fmt.Errorf("remote command timed out after %s", sshExecTimeout)
		}
	}
}

// upload writes one local file to the remote path via SFTP, creating parent
// directories.
func (c *sshClient) upload(localPath, remotePath string) error {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", localPath, err)
	}
	sftpClient, err := sftp.NewClient(c.client)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer sftpClient.Close()
	remoteFile, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("sftp create %s: %w", remotePath, err)
	}
	if _, err := remoteFile.Write(content); err != nil {
		remoteFile.Close()
		return fmt.Errorf("sftp write %s: %w", remotePath, err)
	}
	return remoteFile.Close()
}

// uploadDir copies one local directory tree to the remote path via SFTP,
// creating parent directories as needed. Used for the bundled private Node
// distribution (docs/next/runner-install.md decision 4: the bundle carries
// Node, so targets need no Node.js installed).
func (c *sshClient) uploadDir(localDir, remoteDir string) error {
	sftpClient, err := sftp.NewClient(c.client)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer sftpClient.Close()

	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, entry := range entries {
			local := filepath.Join(dir, entry.Name())
			remote := filepath.ToSlash(filepath.Join(dir, entry.Name()))
			// Recompute the remote path relative to the local root: replace
			// the local prefix with the remote root on every path.
			rel, err := filepath.Rel(localDir, local)
			if err != nil {
				return err
			}
			remote = remoteDir + "/" + filepath.ToSlash(rel)
			if entry.IsDir() {
				if err := sftpClient.MkdirAll(remote); err != nil {
					return fmt.Errorf("sftp mkdir %s: %w", remote, err)
				}
				if err := walk(local); err != nil {
					return err
				}
				continue
			}
			// Skip links; the Node distribution has none that matter.
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			content, err := os.ReadFile(local)
			if err != nil {
				return fmt.Errorf("reading %s: %w", local, err)
			}
			remoteFile, err := sftpClient.Create(remote)
			if err != nil {
				return fmt.Errorf("sftp create %s: %w", remote, err)
			}
			if _, err := remoteFile.Write(content); err != nil {
				remoteFile.Close()
				return fmt.Errorf("sftp write %s: %w", remote, err)
			}
			remoteFile.Close()
		}
		return nil
	}
	if err := sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", remoteDir, err)
	}
	return walk(localDir)
}
