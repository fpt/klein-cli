package tool

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/message"
	"github.com/pkg/errors"
)

func TestFileSystemToolManager_SecurityFeatures(t *testing.T) {
	// Create separate directories for proper isolation testing
	tempDir := t.TempDir()
	allowedSubDir := filepath.Join(tempDir, "allowed")
	workingDir := allowedSubDir // Set working directory to the allowed directory

	// Create a forbidden directory OUTSIDE the allowed/working directory
	tempParent := filepath.Dir(tempDir)
	forbiddenDir := filepath.Join(tempParent, "forbidden_external")

	if err := os.MkdirAll(allowedSubDir, 0755); err != nil {
		t.Fatalf("Failed to create test directories: %v", err)
	}
	if err := os.MkdirAll(forbiddenDir, 0755); err != nil {
		t.Fatalf("Failed to create test directories: %v", err)
	}

	// Create test files
	testFile := filepath.Join(allowedSubDir, "test.txt")
	secretFile := filepath.Join(allowedSubDir, "secret.env")
	forbiddenFile := filepath.Join(forbiddenDir, "forbidden.txt")

	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	if err := os.WriteFile(secretFile, []byte("API_KEY=secret123"), 0644); err != nil {
		t.Fatalf("Failed to create secret file: %v", err)
	}
	if err := os.WriteFile(forbiddenFile, []byte("forbidden content"), 0644); err != nil {
		t.Fatalf("Failed to create forbidden file: %v", err)
	}

	// Create filesystem tool manager with restricted access
	// Note: working directory (allowedSubDir) will be automatically included in allowed directories
	config := repository.FileSystemConfig{
		AllowedDirectories: []string{}, // Empty - only working directory should be allowed
		BlacklistedFiles:   []string{"*.env", "*secret*"},
	}

	fsRepo := infra.NewOSFilesystemRepository()
	manager := NewFileSystemToolManager(fsRepo, config, workingDir)
	ctx := context.Background()

	t.Run("AllowedDirectoryAccess", func(t *testing.T) {
		// Should be able to read allowed file
		result, err := manager.handleReadFile(ctx, map[string]any{
			"path": testFile,
		})
		if err != nil {
			t.Errorf("Expected success reading allowed file, got error: %v", err)
		}
		if result.Error != "" {
			t.Errorf("Expected success, got error: %s", result.Error)
		}
		if !strings.Contains(result.Text, "test content") {
			t.Errorf("Expected file content, got: %s", result.Text)
		}
	})

	t.Run("ForbiddenDirectoryAccess", func(t *testing.T) {
		// Should not be able to read file in forbidden directory
		result, err := manager.handleReadFile(ctx, map[string]any{
			"path": forbiddenFile,
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error == "" {
			t.Error("Expected access denied error for forbidden directory")
		}
		// Can be blocked at either path resolution or directory allowlist level
		if !strings.Contains(result.Error, "outside working directory") && !strings.Contains(result.Error, "not within allowed directories") {
			t.Errorf("Expected directory access error, got: %s", result.Error)
		}
	})

	t.Run("BlacklistedFileAccess", func(t *testing.T) {
		// Should not be able to read blacklisted file
		result, err := manager.handleReadFile(ctx, map[string]any{
			"path": secretFile,
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error == "" {
			t.Error("Expected access denied error for blacklisted file")
		}
		if !strings.Contains(result.Error, "blacklisted") {
			t.Errorf("Expected blacklist error, got: %s", result.Error)
		}
	})

	t.Run("ReadWriteSemantics", func(t *testing.T) {
		// Test 1: Writing a new file should succeed without prior read
		newFile := filepath.Join(allowedSubDir, "new_file.txt")

		result, err := manager.handleWriteFile(ctx, map[string]any{
			"path":    newFile,
			"content": "new file content",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Errorf("Expected success writing new file, got error: %s", result.Error)
		}

		// Test 2: Writing to existing file without prior read should fail
		existingFile := filepath.Join(allowedSubDir, "existing_file.txt")
		if err := os.WriteFile(existingFile, []byte("original content"), 0644); err != nil {
			t.Fatalf("Failed to create existing file: %v", err)
		}

		result, err = manager.handleWriteFile(ctx, map[string]any{
			"path":    existingFile,
			"content": "modified content",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error == "" {
			t.Error("Expected read-write semantics violation for existing file")
		}
		if !strings.Contains(result.Error, "was not read before write") {
			t.Errorf("Expected read-write semantics error, got: %s", result.Error)
		}

		// Test 3: Read existing file first, then write should succeed
		_, err = manager.handleReadFile(ctx, map[string]any{
			"path": existingFile,
		})
		if err != nil {
			t.Errorf("Unexpected error reading for write semantics: %v", err)
		}

		result, err = manager.handleWriteFile(ctx, map[string]any{
			"path":    existingFile,
			"content": "modified content after read",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Errorf("Expected write success after read, got error: %s", result.Error)
		}

		// Test 4: Subsequent writes should succeed without re-reading (timestamp updated after write)
		result, err = manager.handleWriteFile(ctx, map[string]any{
			"path":    existingFile,
			"content": "second modification without re-read",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Errorf("Expected subsequent write success, got error: %s", result.Error)
		}

		// Test 5: Edit operations should also succeed after write (timestamp updated)
		result, err = manager.handleEnhancedEdit(ctx, map[string]any{
			"file_path":  existingFile,
			"old_string": "second modification without re-read",
			"new_string": "edited content after write",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Errorf("Expected edit success after write, got error: %s", result.Error)
		}

		// Test 6: Multiple sequential edits should work
		result, err = manager.handleEnhancedEdit(ctx, map[string]any{
			"file_path":  existingFile,
			"old_string": "edited content after write",
			"new_string": "final edited content",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Errorf("Expected sequential edit success, got error: %s", result.Error)
		}
	})

	t.Run("TimestampValidation", func(t *testing.T) {
		timestampFile := filepath.Join(allowedSubDir, "timestamp_test.txt")

		// Create and read a file
		if err := os.WriteFile(timestampFile, []byte("original"), 0644); err != nil {
			t.Fatalf("Failed to create timestamp test file: %v", err)
		}

		// Read the file to establish timestamp
		_, err := manager.handleReadFile(ctx, map[string]any{
			"path": timestampFile,
		})
		if err != nil {
			t.Fatalf("Failed to read file for timestamp test: %v", err)
		}

		// Simulate external modification
		time.Sleep(10 * time.Millisecond) // Ensure different timestamp
		if err := os.WriteFile(timestampFile, []byte("externally modified"), 0644); err != nil {
			t.Fatalf("Failed to modify file externally: %v", err)
		}

		// Attempt to write - should fail due to external modification
		result, err := manager.handleWriteFile(ctx, map[string]any{
			"path":    timestampFile,
			"content": "my content",
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.Error == "" {
			t.Error("Expected timestamp validation failure")
		}
		if !strings.Contains(result.Error, "was modified after last read") {
			t.Errorf("Expected timestamp error, got: %s", result.Error)
		}
	})
}

func TestFileSystemToolManager_ToolRegistration(t *testing.T) {
	// Create a temporary directory for this test
	tempDir := t.TempDir()

	config := repository.FileSystemConfig{
		AllowedDirectories: []string{"."},
		BlacklistedFiles:   []string{},
	}

	fsRepo := infra.NewOSFilesystemRepository()
	manager := NewFileSystemToolManager(fsRepo, config, tempDir)

	// Verify expected tools
	expectedTools := []string{
		"Read",
		"Write",
		"Edit",
		"LS",
		"MultiEdit",
	}

	toolsMap := manager.GetTools()
	if len(toolsMap) != len(expectedTools) {
		t.Errorf("Expected %d tools, got %d", len(expectedTools), len(toolsMap))
	}

	for _, expectedName := range expectedTools {
		tool, exists := manager.GetTool(message.ToolName(expectedName))
		if !exists {
			t.Errorf("Expected tool %s not found", expectedName)
		}
		if tool.Name() != message.ToolName(expectedName) {
			t.Errorf("Tool name mismatch: expected %s, got %s", expectedName, tool.Name())
		}
	}
}

func TestFileSystemToolManager_ResolvePath(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Create filesystem tool manager with working directory set to tempDir
	config := repository.FileSystemConfig{
		AllowedDirectories: []string{tempDir},
	}
	fsRepo := infra.NewOSFilesystemRepository()
	manager := NewFileSystemToolManager(fsRepo, config, tempDir)

	tests := []struct {
		name        string
		inputPath   string
		expectError bool
		checkSuffix string
	}{
		{
			name:        "RelativePath",
			inputPath:   "test.txt",
			expectError: false,
			checkSuffix: filepath.Join(tempDir, "test.txt"),
		},
		{
			name:        "AbsolutePath",
			inputPath:   filepath.Join(tempDir, "absolute.txt"),
			expectError: false,
			checkSuffix: filepath.Join(tempDir, "absolute.txt"),
		},
		{
			name:        "DotPath",
			inputPath:   ".",
			expectError: false,
			checkSuffix: tempDir,
		},
		{
			name:        "SubdirectoryPath",
			inputPath:   "subdir/file.txt",
			expectError: false,
			checkSuffix: filepath.Join(tempDir, "subdir", "file.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := manager.resolvePath(tt.inputPath)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none for path %s", tt.inputPath)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for path %s: %v", tt.inputPath, err)
				return
			}

			if !strings.HasSuffix(resolved, tt.checkSuffix) {
				t.Errorf("Expected resolved path to end with %s, but got %s", tt.checkSuffix, resolved)
			}

			// Verify that resolved path is absolute
			if !filepath.IsAbs(resolved) {
				t.Errorf("Expected absolute path but got relative path: %s", resolved)
			}
		})
	}
}

func TestEnsureWorkingDirectoryInAllowedList(t *testing.T) {
	tests := []struct {
		name                  string
		configuredDirectories []string
		absWorkingDir         string
		expected              []string
		description           string
	}{
		{
			name:                  "empty_configured_directories",
			configuredDirectories: []string{},
			absWorkingDir:         "/tmp/working",
			expected:              []string{"/tmp/working"},
			description:           "Should add working directory when configured directories is empty",
		},
		{
			name:                  "nil_configured_directories",
			configuredDirectories: nil,
			absWorkingDir:         "/tmp/working",
			expected:              []string{"/tmp/working"},
			description:           "Should add working directory when configured directories is nil",
		},
		{
			name:                  "working_dir_already_present_exact_match",
			configuredDirectories: []string{"/tmp/working", "/other/path"},
			absWorkingDir:         "/tmp/working",
			expected:              []string{"/tmp/working", "/other/path"},
			description:           "Should not duplicate working directory when already present (exact match)",
		},
		{
			name:                  "working_dir_not_present",
			configuredDirectories: []string{"/some/path", "/another/path"},
			absWorkingDir:         "/tmp/working",
			expected:              []string{"/some/path", "/another/path", "/tmp/working"},
			description:           "Should add working directory when not present in configured directories",
		},
		{
			name:                  "working_dir_present_as_relative_path",
			configuredDirectories: []string{"./working", "/other/path"},
			absWorkingDir:         getCurrentDir() + "/working",
			expected:              []string{"./working", "/other/path"},
			description:           "Should not duplicate when working directory present as relative path",
		},
		{
			name:                  "configured_dirs_with_invalid_paths",
			configuredDirectories: []string{"", "/valid/path", "/tmp/working"},
			absWorkingDir:         "/tmp/working",
			expected:              []string{"", "/valid/path", "/tmp/working"},
			description:           "Should handle invalid paths gracefully and not duplicate valid working dir",
		},
		{
			name:                  "multiple_similar_paths",
			configuredDirectories: []string{"/tmp/work", "/tmp/working-other", "/other/path"},
			absWorkingDir:         "/tmp/working",
			expected:              []string{"/tmp/work", "/tmp/working-other", "/other/path", "/tmp/working"},
			description:           "Should add working directory even when similar paths exist",
		},
		{
			name:                  "preserve_original_order",
			configuredDirectories: []string{"/z/path", "/a/path", "/m/path"},
			absWorkingDir:         "/tmp/working",
			expected:              []string{"/z/path", "/a/path", "/m/path", "/tmp/working"},
			description:           "Should preserve the original order of configured directories",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureWorkingDirectoryInAllowedList(tt.configuredDirectories, tt.absWorkingDir)

			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ensureWorkingDirectoryInAllowedList() = %v, want %v\nDescription: %s",
					result, tt.expected, tt.description)
			}

			// Verify that the original slice was not modified
			if tt.configuredDirectories != nil {
				// Create a copy to compare against
				originalCopy := make([]string, len(tt.configuredDirectories))
				copy(originalCopy, tt.configuredDirectories)
				if !reflect.DeepEqual(tt.configuredDirectories, originalCopy) {
					t.Errorf("Original configured directories slice was modified. This should not happen.")
				}
			}
		})
	}
}

func TestEnsureWorkingDirectoryInAllowedList_EdgeCases(t *testing.T) {
	t.Run("working_dir_is_empty", func(t *testing.T) {
		result := ensureWorkingDirectoryInAllowedList([]string{"/some/path"}, "")
		expected := []string{"/some/path", ""}

		if !reflect.DeepEqual(result, expected) {
			t.Errorf("ensureWorkingDirectoryInAllowedList() with empty working dir = %v, want %v", result, expected)
		}
	})

	t.Run("case_sensitivity", func(t *testing.T) {
		// On case-sensitive filesystems, these should be treated as different
		result := ensureWorkingDirectoryInAllowedList([]string{"/TMP/WORKING"}, "/tmp/working")

		// Should add the working directory because case matters on most Unix systems
		if len(result) != 2 {
			t.Errorf("ensureWorkingDirectoryInAllowedList() should treat case-sensitive paths as different")
		}
	})

	t.Run("working_dir_with_trailing_slash", func(t *testing.T) {
		// Test that trailing slashes don't affect matching
		configuredDirs := []string{"/tmp/working/"}
		workingDir := "/tmp/working"

		result := ensureWorkingDirectoryInAllowedList(configuredDirs, workingDir)

		// Should not duplicate because filepath.Abs should normalize paths
		if len(result) != 1 {
			t.Errorf("ensureWorkingDirectoryInAllowedList() should handle trailing slashes correctly, got %d items: %v", len(result), result)
		}
	})
}

func TestEnsureWorkingDirectoryInAllowedList_InputImmutability(t *testing.T) {
	t.Run("original_slice_not_modified", func(t *testing.T) {
		originalDirs := []string{"/path1", "/path2"}
		originalCopy := make([]string, len(originalDirs))
		copy(originalCopy, originalDirs)

		workingDir := "/tmp/working"

		result := ensureWorkingDirectoryInAllowedList(originalDirs, workingDir)

		// Verify original slice is unchanged
		if !reflect.DeepEqual(originalDirs, originalCopy) {
			t.Errorf("Original input slice was modified: got %v, want %v", originalDirs, originalCopy)
		}

		// Verify result has the working directory added
		expected := []string{"/path1", "/path2", "/tmp/working"}
		if !reflect.DeepEqual(result, expected) {
			t.Errorf("Result incorrect: got %v, want %v", result, expected)
		}

		// Verify modifying the result doesn't affect the original
		result[0] = "/modified"
		if originalDirs[0] == "/modified" {
			t.Errorf("Modifying result affected original slice")
		}
	})
}

// getCurrentDir returns the current working directory for testing relative paths
func getCurrentDir() string {
	dir, err := filepath.Abs(".")
	if err != nil {
		return "." // Fallback for tests
	}
	return dir
}

func TestFileSystemToolManager_IsPathAllowed(t *testing.T) {
	tempDir := t.TempDir()
	allowedDir := filepath.Join(tempDir, "allowed")
	forbiddenDir := filepath.Join(tempDir, "forbidden")

	if err := os.MkdirAll(allowedDir, 0755); err != nil {
		t.Fatalf("Failed to create test directories: %v", err)
	}
	if err := os.MkdirAll(forbiddenDir, 0755); err != nil {
		t.Fatalf("Failed to create test directories: %v", err)
	}

	// Create manager with specific allowed directories
	config := repository.FileSystemConfig{
		AllowedDirectories: []string{allowedDir},
		BlacklistedFiles:   []string{},
	}
	// Use a different working directory to test the allowlist logic specifically
	fsRepo := infra.NewOSFilesystemRepository()
	manager := NewFileSystemToolManager(fsRepo, config, allowedDir)

	t.Run("allowed_path_should_pass", func(t *testing.T) {
		allowedFile := filepath.Join(allowedDir, "test.txt")
		err := manager.isPathAllowed(allowedFile)
		if err != nil {
			t.Errorf("Expected allowed path to pass, got error: %v", err)
		}
	})

	t.Run("forbidden_path_should_return_errNotInAllowedDirectory", func(t *testing.T) {
		forbiddenFile := filepath.Join(forbiddenDir, "test.txt")
		err := manager.isPathAllowed(forbiddenFile)

		if err == nil {
			t.Error("Expected error for forbidden path")
		}

		if !errors.Is(err, errNotInAllowedDirectory) {
			t.Errorf("Expected errNotInAllowedDirectory, got: %v", err)
		}
	})
}
