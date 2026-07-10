package control

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultSocketPath resolves the control socket path:
// $PORTLESS_SOCKET → $XDG_RUNTIME_DIR/portless.sock → OS cache dir →
// /tmp/portless-$UID.sock.
func DefaultSocketPath() string {
	if p := os.Getenv("PORTLESS_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "portless.sock")
	}
	if runtime.GOOS == "darwin" {
		if d, err := os.UserCacheDir(); err == nil {
			return filepath.Join(d, "portless", "portless.sock")
		}
	}
	// A per-user subdirectory (created 0700 by EnsureSocketDir) rather than
	// the socket sitting directly in world-writable /tmp.
	return filepath.Join(os.TempDir(), fmt.Sprintf("portless-%d", os.Getuid()), "portless.sock")
}

// EnsureSocketDir creates the socket's parent directory with 0700 and removes
// a stale socket file at path, returning an error if something else lives
// there.
func EnsureSocketDir(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("control: create socket dir: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control: %s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("control: remove stale socket: %w", err)
		}
	}
	return nil
}
