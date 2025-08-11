package infra

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fpt/klein-cli/internal/repository"
)

// DefaultFileSystemConfig returns a more permissive configuration for development
func DefaultFileSystemConfig(workingDir string) repository.FileSystemConfig {
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		absWorkingDir = workingDir
	}

	return repository.FileSystemConfig{
		// Simple and secure: only allow operations under the working directory
		// This automatically handles any working directory (project root, test dir, etc.)
		AllowedDirectories: []string{
			absWorkingDir,
		},
		// More comprehensive blacklist for development
		BlacklistedFiles: []string{
			".env",
			".env.*",
			"*.key",
			"*.pem",
			"*.crt",
			"*secret*",
			"*password*",
			"*token*",
			"*api_key*",
			".aws/credentials",
			".aws/config",
			".ssh/id_*",
			".ssh/known_hosts",
			"*.p12",
			"*.pfx",
			"config.json",
			"secrets.json",
			"credentials.json",
			".netrc",
			".dockercfg",
			".docker/config.json",
			".npmrc",
			".yarnrc",
			".gitconfig",
			"*.sqlite",
			"*.db",
			"node_modules/*",
			".git/*",
			"vendor/*",
			"*.log",
		},
	}
}

// OSFilesystemRepository implements repository.FilesystemRepository using os package
type OSFilesystemRepository struct{}

// NewOSFilesystemRepository creates a new OS-based filesystem repository
func NewOSFilesystemRepository() repository.FilesystemRepository {
	return &OSFilesystemRepository{}
}

// ReadFile reads the contents of a file
func (r *OSFilesystemRepository) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile writes data to a file
func (r *OSFilesystemRepository) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// Stat returns file information
func (r *OSFilesystemRepository) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

// ReadDir reads directory contents
func (r *OSFilesystemRepository) ReadDir(ctx context.Context, path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}

// Exists checks if a file or directory exists
func (r *OSFilesystemRepository) Exists(ctx context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// IsDir checks if path is a directory
func (r *OSFilesystemRepository) IsDir(ctx context.Context, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// IsRegular checks if path is a regular file
func (r *OSFilesystemRepository) IsRegular(ctx context.Context, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
