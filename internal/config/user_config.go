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

// GetProjectTaskFile returns the task file path for a specific project
func (c *UserConfig) GetProjectTaskFile(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, "tasks.json"), nil
}

// GetProjectSessionFile returns the legacy single-session file path for a
// project. Sessions now live one-file-per-run under GetProjectSessionsDir; this
// path is only still consulted so an existing conversation survives the upgrade
// (see migrateLegacySession).
func (c *UserConfig) GetProjectSessionFile(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}

	return filepath.Join(projectDir, "session.json"), nil
}

// sessionFileExt is the suffix identifying a session file inside the sessions
// directory. Sidecars (e.g. "<session>.json.codex-thread") deliberately do not
// match it, so they are never mistaken for sessions of their own.
const sessionFileExt = ".json"

// GetProjectSessionsDir returns the directory holding a project's session files,
// creating it if needed. Each interactive run writes its own file here, so
// starting fresh never overwrites a previous conversation.
func (c *UserConfig) GetProjectSessionsDir(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}
	sessionsDir := filepath.Join(projectDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create sessions directory: %w", err)
	}
	if err := c.migrateLegacySession(projectDir, sessionsDir); err != nil {
		return "", err
	}
	return sessionsDir, nil
}

// NewProjectSessionFile returns the path for a fresh session. The file is *not*
// created here: an interactive run that exits without a single exchange should
// leave nothing behind, or `--continue` would resume that empty session instead
// of the real conversation before it.
func (c *UserConfig) NewProjectSessionFile(projectPath string) (string, error) {
	sessionsDir, err := c.GetProjectSessionsDir(projectPath)
	if err != nil {
		return "", err
	}
	// Nanosecond precision so two shells starting in the same second cannot
	// collide on a name and silently share one file.
	name := time.Now().Format("20060102T150405.000000000") + sessionFileExt
	return filepath.Join(sessionsDir, name), nil
}

// LatestProjectSessionFile returns the most recently modified session file, or
// "" when the project has none yet.
//
// Ordering is by mtime rather than by the timestamp in the name, because what
// `--continue` should resume is the session most recently *used* — a session
// resumed yesterday and worked in today is the one you mean, whenever it began.
func (c *UserConfig) LatestProjectSessionFile(projectPath string) (string, error) {
	sessionsDir, err := c.GetProjectSessionsDir(projectPath)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read sessions directory %s: %w", sessionsDir, err)
	}

	var latest string
	var latestMod time.Time
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != sessionFileExt {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue // vanished mid-scan; not a reason to fail the whole lookup
		}
		if latest == "" || info.ModTime().After(latestMod) {
			latest, latestMod = filepath.Join(sessionsDir, entry.Name()), info.ModTime()
		}
	}
	return latest, nil
}

// migrateLegacySession moves a pre-existing single session.json into the
// sessions directory so the conversation in flight at upgrade time stays
// resumable with `--continue`. Its mtime is preserved by the rename, which is
// what LatestProjectSessionFile orders on.
//
// Only ever runs when the sessions directory is empty: once a project has real
// per-run sessions, a stale session.json must not jump ahead of them.
func (c *UserConfig) migrateLegacySession(projectDir, sessionsDir string) error {
	legacy := filepath.Join(projectDir, "session.json")
	if !isRegularFile(legacy) || dirHasEntries(sessionsDir) {
		return nil
	}

	moved := filepath.Join(sessionsDir, "migrated-session"+sessionFileExt)
	if err := os.Rename(legacy, moved); err != nil {
		return fmt.Errorf("failed to migrate legacy session file: %w", err)
	}
	// The codex thread sidecar is keyed to the session path, so it has to travel
	// with it or a resumed session would start a new codex thread.
	if err := os.Rename(legacy+".codex-thread", moved+".codex-thread"); err != nil && !os.IsNotExist(err) {
		pkgLogger.NewComponentLogger("user-config").WarnWithIntention(
			pkgLogger.IntentionWarning, "Failed to migrate codex thread sidecar", "error", err)
	}
	return nil
}

// isRegularFile reports whether path is an existing non-directory. A stat error
// of any kind counts as "not there": missing or unreadable, there is nothing
// here worth migrating.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirHasEntries reports whether dir contains anything. A read error counts as
// "yes" on purpose — that is the conservative direction, skipping the migration
// rather than letting a legacy file jump ahead of sessions we failed to see.
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err != nil || len(entries) > 0
}

// GetProjectHistoryFile returns the readline history file path for a specific project
func (c *UserConfig) GetProjectHistoryFile(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(projectDir, "history.txt"), nil
}

// GetProjectMemoryDir returns the memory directory path for a specific project.
// Creates $HOME/.klein/projects/{project-hash}/memory/ if it doesn't exist.
func (c *UserConfig) GetProjectMemoryDir(projectPath string) (string, error) {
	projectDir, err := c.GetProjectDataDir(projectPath)
	if err != nil {
		return "", err
	}
	memoryDir := filepath.Join(projectDir, "memory")
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create memory directory: %w", err)
	}
	return memoryDir, nil
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
