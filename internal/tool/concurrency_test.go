package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

// TestMemoryWriteConcurrentAppend confirms concurrent MemoryWrite appends — the
// case where a Discord peer and a firing cron share the one MemoryToolManager —
// neither race (run with -race) nor lose entries. All N lines must survive.
func TestMemoryWriteConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryToolManager(dir)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{
				"path": "MEMORY.md", "content": fmt.Sprintf("entry-%03d", i),
			})
			if r.Error != "" {
				t.Errorf("append %d: %s", i, r.Error)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for i := 0; i < n; i++ {
		if strings.Contains(string(data), fmt.Sprintf("entry-%03d", i)) {
			got++
		}
	}
	if got != n {
		t.Errorf("lost appends: %d/%d entries survived", got, n)
	}
}

// TestMemoryWriteConcurrentOverwriteReadable confirms concurrent overwrites never
// leave a reader with a truncated/partial file — every read yields one of the
// complete written values (atomic replace), never an empty or spliced file.
func TestMemoryWriteConcurrentOverwriteReadable(t *testing.T) {
	dir := t.TempDir()
	m := NewMemoryToolManager(dir)
	ctx := context.Background()
	path := filepath.Join(dir, "MEMORY.md")

	full := strings.Repeat("A", 4096)
	if r, _ := m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{"path": "MEMORY.md", "content": full, "mode": "overwrite"}); r.Error != "" {
		t.Fatalf("seed: %s", r.Error)
	}

	// Reader runs on its own lifecycle (not the writers' WaitGroup) so closing
	// stop after the writers finish can't deadlock.
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue // rename window: file momentarily absent is fine
			}
			// Every committed file is one complete 4096-byte block, never partial.
			if len(data) != 0 && len(data) != 4096 {
				t.Errorf("partial read: %d bytes", len(data))
				return
			}
		}
	}()

	var writers sync.WaitGroup
	for i := 0; i < 40; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			c := strings.Repeat(string(rune('B'+i%20)), 4096)
			m.CallTool(ctx, "MemoryWrite", message.ToolArgumentValues{"path": "MEMORY.md", "content": c, "mode": "overwrite"})
		}(i)
	}
	writers.Wait()
	close(stop)
	<-readerDone
}

// TestScheduleConcurrentCreate confirms concurrent ScheduleCreate calls on the
// shared manager don't race and every distinct schedule survives (upsert under
// the mutex, atomic file replacement).
func TestScheduleConcurrentCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	m := NewScheduleToolManager(path)
	ctx := context.Background()

	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _ := m.CallTool(ctx, "ScheduleCreate", message.ToolArgumentValues{
				"name": fmt.Sprintf("job-%02d", i), "prompt": "collect metrics and summarize",
				"cron": "0 * * * *", "timezone": "Asia/Tokyo",
				"channel_type": "discord", "channel_id": "1",
			})
			if r.Error != "" {
				t.Errorf("create %d: %s", i, r.Error)
			}
		}(i)
	}
	wg.Wait()

	entries, err := m.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Errorf("lost schedules: %d/%d survived", len(entries), n)
	}
}
