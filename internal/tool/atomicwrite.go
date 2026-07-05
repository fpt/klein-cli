package tool

import (
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path atomically: it writes to a temp file in
// the same directory and renames it into place. os.Rename is atomic on POSIX,
// so a concurrent reader (e.g. the gateway scheduler polling schedules.json, or
// an agent reading MEMORY.md) never observes a half-written file. The temp file
// shares the destination directory so the rename stays on one filesystem.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we fail before the rename succeeds.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
