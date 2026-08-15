package sidecar

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// SocketEnvVar names the environment variable the core uses to tell a plugin
// where its socket lives (spec §3).
const SocketEnvVar = "KEYSTONE_PLUGIN_SOCKET"

// SocketPathFromEnv reads the socket path the core assigned.
func SocketPathFromEnv() (string, error) {
	path := os.Getenv(SocketEnvVar)
	if path == "" {
		return "", fmt.Errorf("sidecar: %s is not set — a plugin is started by the core, which supplies it", SocketEnvVar)
	}
	return path, nil
}

// DialUnix connects to a socket, honouring ctx for the dial itself.
func DialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("sidecar: dialling %s: %w", path, err)
	}
	return conn, nil
}

// ListenUnix creates a listening socket.
//
// It removes a stale socket file first. A unix socket left behind by a process
// that was killed makes bind fail with "address already in use" — for a file
// that nothing is listening on — and a plugin restarted after a crash would
// never come back. The check is narrow: only a socket file is removed, and
// only if nothing answers on it.
//
// The socket is created with mode 0600, so it is reachable only by the user
// the plugin runs as. That is the whole of v1's access control; see
// OPEN_QUESTIONS.md §1.
func ListenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("sidecar: creating the socket directory: %w", err)
	}

	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sidecar: listening on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("sidecar: restricting %s: %w", path, err)
	}
	return ln, nil
}

// removeStaleSocket deletes a socket file that nothing is listening on.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sidecar: inspecting %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		// Something that is not a socket is in the way. Removing it would be
		// overreach — it might be a file someone cares about.
		return fmt.Errorf("sidecar: %s exists and is not a socket", path)
	}

	// If a live process is listening, this is a genuine conflict rather than a
	// leftover, and silently unlinking it would steal the socket from a
	// running plugin.
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return fmt.Errorf("sidecar: %s is already in use by a live listener", path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("sidecar: removing the stale socket %s: %w", path, err)
	}
	return nil
}
