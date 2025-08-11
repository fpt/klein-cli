package repository

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TodoItem represents a single todo item
type TodoItem struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`   // pending, in_progress, completed (compat: accepts 'done')
	Priority string `json:"priority"` // high, medium, low
	Created  string `json:"created"`
	Updated  string `json:"updated"`
}

// TodoRepository abstracts todo data persistence
type TodoRepository interface {
	GetItems() []TodoItem
	SetItems(items []TodoItem)
	GetUpdated() string
	SetUpdated(updated string)
	Load() error
	Save() error
}

// FileTodoRepository represents file-persisted todo repository
type FileTodoRepository struct {
	Items    []TodoItem `json:"items"`
	Updated  string     `json:"updated"`
	mu       sync.RWMutex
	filePath string
}

// InMemoryTodoRepository represents in-memory-only todo repository
type InMemoryTodoRepository struct {
	Items   []TodoItem `json:"items"`
	Updated string     `json:"updated"`
	mu      sync.RWMutex
}

// NewFileTodoRepository creates a new file-based todo repository
func NewFileTodoRepository(filePath string) *FileTodoRepository {
	return &FileTodoRepository{
		Items:    make([]TodoItem, 0),
		filePath: filePath,
	}
}

// NewInMemoryTodoRepository creates a new in-memory todo repository
func NewInMemoryTodoRepository() *InMemoryTodoRepository {
	return &InMemoryTodoRepository{
		Items: make([]TodoItem, 0),
	}
}

// FileTodoRepository methods
func (fr *FileTodoRepository) GetItems() []TodoItem {
	fr.mu.RLock()
	defer fr.mu.RUnlock()
	return fr.Items
}

func (fr *FileTodoRepository) SetItems(items []TodoItem) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	fr.Items = items
}

func (fr *FileTodoRepository) GetUpdated() string {
	fr.mu.RLock()
	defer fr.mu.RUnlock()
	return fr.Updated
}

func (fr *FileTodoRepository) SetUpdated(updated string) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	fr.Updated = updated
}

func (fr *FileTodoRepository) Load() error {
	if fr.filePath == "" {
		return fmt.Errorf("no file path specified")
	}

	data, err := os.ReadFile(fr.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet, start with empty state
			return nil
		}
		return err
	}

	fr.mu.Lock()
	defer fr.mu.Unlock()

	if err := json.Unmarshal(data, fr); err != nil {
		return err
	}
	// Normalize legacy statuses on load
	for i := range fr.Items {
		if fr.Items[i].Status == "done" {
			fr.Items[i].Status = "completed"
		}
	}
	return nil
}

func (fr *FileTodoRepository) Save() error {
	if fr.filePath == "" {
		return fmt.Errorf("no file path specified")
	}

	fr.mu.RLock()
	data, err := json.MarshalIndent(fr, "", "  ")
	fr.mu.RUnlock()

	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(fr.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(fr.filePath, data, 0644)
}

// InMemoryTodoRepository methods
func (mr *InMemoryTodoRepository) GetItems() []TodoItem {
	mr.mu.RLock()
	defer mr.mu.RUnlock()
	return mr.Items
}

func (mr *InMemoryTodoRepository) SetItems(items []TodoItem) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	mr.Items = items
}

func (mr *InMemoryTodoRepository) GetUpdated() string {
	mr.mu.RLock()
	defer mr.mu.RUnlock()
	return mr.Updated
}

func (mr *InMemoryTodoRepository) SetUpdated(updated string) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	mr.Updated = updated
}

func (mr *InMemoryTodoRepository) Load() error {
	// No-op for in-memory repository
	return nil
}

func (mr *InMemoryTodoRepository) Save() error {
	// No-op for in-memory repository
	return nil
}
