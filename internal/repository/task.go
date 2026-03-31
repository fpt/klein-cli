package repository

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TaskItem represents a single task with dependency tracking.
type TaskItem struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`              // pending | in_progress | completed | deleted
	BlockedBy   []string `json:"blocked_by,omitempty"` // IDs of tasks that must complete first
	Blocks      []string `json:"blocks,omitempty"`     // IDs of tasks that this task unblocks
	Created     string   `json:"created"`
	Updated     string   `json:"updated"`
}

// TaskRepository abstracts task data persistence.
type TaskRepository interface {
	GetItems() []TaskItem
	SetItems(items []TaskItem)
	GetUpdated() string
	SetUpdated(updated string)
	Load() error
	Save() error
}

// taskStore is the on-disk JSON envelope.
type taskStore struct {
	Items   []TaskItem `json:"items"`
	Updated string     `json:"updated"`
}

// FileTaskRepository persists tasks to a JSON file.
type FileTaskRepository struct {
	store    taskStore
	mu       sync.RWMutex
	filePath string
}

// InMemoryTaskRepository holds tasks in memory only.
type InMemoryTaskRepository struct {
	store taskStore
	mu    sync.RWMutex
}

// NewFileTaskRepository creates a file-backed task repository.
func NewFileTaskRepository(filePath string) *FileTaskRepository {
	return &FileTaskRepository{
		store:    taskStore{Items: make([]TaskItem, 0)},
		filePath: filePath,
	}
}

// NewInMemoryTaskRepository creates an in-memory task repository.
func NewInMemoryTaskRepository() *InMemoryTaskRepository {
	return &InMemoryTaskRepository{
		store: taskStore{Items: make([]TaskItem, 0)},
	}
}

// FileTaskRepository methods

func (r *FileTaskRepository) GetItems() []TaskItem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.Items
}

func (r *FileTaskRepository) SetItems(items []TaskItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store.Items = items
}

func (r *FileTaskRepository) GetUpdated() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.Updated
}

func (r *FileTaskRepository) SetUpdated(updated string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store.Updated = updated
}

func (r *FileTaskRepository) Load() error {
	if r.filePath == "" {
		return fmt.Errorf("no file path specified")
	}
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return json.Unmarshal(data, &r.store)
}

func (r *FileTaskRepository) Save() error {
	if r.filePath == "" {
		return fmt.Errorf("no file path specified")
	}
	r.mu.RLock()
	data, err := json.MarshalIndent(r.store, "", "  ")
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.filePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.filePath, data, 0644)
}

// InMemoryTaskRepository methods

func (r *InMemoryTaskRepository) GetItems() []TaskItem {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.Items
}

func (r *InMemoryTaskRepository) SetItems(items []TaskItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store.Items = items
}

func (r *InMemoryTaskRepository) GetUpdated() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.Updated
}

func (r *InMemoryTaskRepository) SetUpdated(updated string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store.Updated = updated
}

func (r *InMemoryTaskRepository) Load() error { return nil }
func (r *InMemoryTaskRepository) Save() error { return nil }
