package resume

import (
	"path/filepath"
	"testing"
)

func TestCompleteTracksPending(t *testing.T) {
	manager := New(filepath.Join(t.TempDir(), "state.json"), "hash", []string{"net"}, []string{"a.net", "b.net", "c.net"})
	manager.Complete("a.net")
	state := manager.State()
	if state.Completed != 1 {
		t.Fatalf("completed = %d, want 1", state.Completed)
	}
	if len(state.Pending) != 2 {
		t.Fatalf("pending = %v", state.Pending)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	manager := New(path, "hash123", []string{"net"}, []string{"a.net", "b.net"})
	manager.Complete("a.net")
	if err := manager.Save(); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	state := loaded.State()
	if state.InputHash != "hash123" {
		t.Fatalf("input hash = %q", state.InputHash)
	}
	if state.Completed != 1 || len(state.Pending) != 1 || state.Pending[0] != "b.net" {
		t.Fatalf("state = %+v", state)
	}
	if loaded.Finished() {
		t.Fatal("scan should not be finished")
	}
}

func TestMarkFinished(t *testing.T) {
	manager := New(filepath.Join(t.TempDir(), "state.json"), "hash", nil, []string{"a.net"})
	manager.MarkFinished()
	if !manager.Finished() {
		t.Fatal("scan should be finished")
	}
	state := manager.State()
	if len(state.Pending) != 0 || state.Completed != state.Total {
		t.Fatalf("state = %+v", state)
	}
}

func TestSetPending(t *testing.T) {
	manager := New(filepath.Join(t.TempDir(), "state.json"), "hash", nil, []string{"a.net", "b.net", "c.net"})
	manager.SetPending([]string{"c.net"})
	state := manager.State()
	if state.Completed != 2 {
		t.Fatalf("completed = %d, want 2", state.Completed)
	}
	if len(state.Pending) != 1 || state.Pending[0] != "c.net" {
		t.Fatalf("pending = %v", state.Pending)
	}
}

func TestHashTargetsIsOrderIndependent(t *testing.T) {
	left := HashTargets([]string{"a.net", "b.net"})
	right := HashTargets([]string{"b.net", "a.net"})
	if left != right {
		t.Fatal("hash should not depend on order")
	}
}
