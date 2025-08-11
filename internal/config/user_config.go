package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// UserConfig manages per-user configuration and data directories
type UserConfig struct {
	BaseDir     string // $HOME/.klein
	ProjectsDir string // $HOME/.klein/projects
	ConfigFile  string // $HOME/.klein/config.json
}

// DefaultUserConfig creates the default user configuration
func DefaultUserConfig() (*UserConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".klein")

	config := &UserConfig{
		BaseDir:     baseDir,
		ProjectsDir: filepath.Join(baseDir, "projects"),
		ConfigFile:  filepath.Join(baseDir, "config.json"),
	}

	// Ensure directories exist
	if err := config.EnsureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create user directories: %w", err)
	}

	return config, nil
}

// EnsureDirectories creates the user configuration directories if they don't exist
func (c *UserConfig) EnsureDirectories() error {
	dirs := []string{
		c.BaseDir,
		c.ProjectsDir,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}

// GetProjectDataDir returns a project-specific data directory
// Creates $HOME/.klein/projects/{project-hash}/
func (c *UserConfig) GetProjectDataDir(projectPath string) (string, error) {
	// Get absolute path for consistent hashing
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Create a safe directory name from the project path
	projectHash := generateProjectHash(absPath)
	projectDir := filepath.Join(c.ProjectsDir, projectHash)

	// Create project directory
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create project directory: %w", err)
	}

	// Create project info file for reference
	infoFile := filepath.Join(projectDir, "project_info.txt")
	if _, err := os.Stat(infoFile); os.IsNotExist(err) {
		info := fmt.Sprintf("Project Path: %s\nCreated: %s\n", absPath, getCurrentTimestamp())
		if err := os.WriteFile(infoFile, []byte(info), 0644); err != nil {
			// Non-fatal error, just log it
			pkgLogger.NewComponentLogger("user-config").WarnWithIntention(pkgLogger.IntentionWarning, "Failed to create project info file", "error", err)
		}
	}

	return projectDir, nil
}

// GetProjectTodoFile returns the todo file path for a specific project
func (c *UserConfig) GetProjectTodoFile(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}

	return filepath.Join(projectDir, "todos.json"), nil
}

// GetProjectSessionFile returns the session state file path for a specific project
func (c *UserConfig) GetProjectSessionFile(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}

	return filepath.Join(projectDir, "session.json"), nil
}

// GetProjectHistoryFile returns the readline history file path for a specific project
func (c *UserConfig) GetProjectHistoryFile(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, "history.txt"), nil
}

// generateProjectHash creates a safe directory name from a project path
func generateProjectHash(projectPath string) string {
	// Claude Code uses full path with slashes replaced by dashes
	// e.g., /Users/youichi.fujimoto/Documents/scratch/go-llama-code
	// becomes -Users-youichi-fujimoto-Documents-scratch-go-llama-code

	// Convert to slash notation for consistency
	normalizedPath := filepath.ToSlash(projectPath)

	// Replace slashes with dashes
	dashPath := strings.ReplaceAll(normalizedPath, "/", "-")

	// Remove any problematic characters but keep dashes
	result := ""
	for _, r := range dashPath {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			result += string(r)
		case r == '-' || r == '_' || r == '.':
			result += string(r)
		default:
			result += "_"
		}
	}

	return result
}

// getCurrentTimestamp returns the current timestamp as a string
func getCurrentTimestamp() string {
	return getCurrentTime().Format("2006-01-02 15:04:05")
}

// getCurrentTime returns the current time
func getCurrentTime() time.Time {
	return time.Now()
}

// ListProjectDirectories returns all project directories with their info
func (c *UserConfig) ListProjectDirectories() ([]ProjectInfo, error) {
	entries, err := os.ReadDir(c.ProjectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ProjectInfo{}, nil
		}
		return nil, fmt.Errorf("failed to read projects directory: %w", err)
	}

	var projects []ProjectInfo
	for _, entry := range entries {
		if entry.IsDir() {
			projectDir := filepath.Join(c.ProjectsDir, entry.Name())
			infoFile := filepath.Join(projectDir, "project_info.txt")

			info := ProjectInfo{
				Hash: entry.Name(),
				Dir:  projectDir,
			}

			// Try to read project info
			if data, err := os.ReadFile(infoFile); err == nil {
				// Parse project info from the file
				lines := strings.Split(string(data), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "Project Path: ") {
						info.Path = strings.TrimPrefix(line, "Project Path: ")
					}
				}
			}

			projects = append(projects, info)
		}
	}

	return projects, nil
}

// ProjectInfo contains information about a tracked project
type ProjectInfo struct {
	Hash string // Directory name hash
	Path string // Original project path
	Dir  string // Full path to project data directory
}
