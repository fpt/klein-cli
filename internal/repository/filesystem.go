package repository

import (
	"context"
	"io/fs"
)

// FileSystemConfig holds configuration for the filesystem tool manager
type FileSystemConfig struct {
	AllowedDirectories []string `json:"allowed_directories"` // Paths where file operations are allowed
	BlacklistedFiles   []string `json:"blacklisted_files"`   // Files that cannot be read
}

// FilesystemRepository abstracts filesystem operations for the filesystem tool manager
type FilesystemRepository interface {
	// File operations
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
	Stat(ctx context.Context, path string) (fs.FileInfo, error)

	// Directory operations
	ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error)

	// File existence and metadata
	Exists(ctx context.Context, path string) (bool, error)
	IsDir(ctx context.Context, path string) (bool, error)
	IsRegular(ctx context.Context, path string) (bool, error)
}
