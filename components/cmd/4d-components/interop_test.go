package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The archive store and the lock file are shared between the Python reference and this implementation, so a
// store written by one must be usable offline by the other.
func TestArchiveStoreAndLockAreInterchangeable(t *testing.T) {
	for _, writer := range []int{0, 1} {
		name := map[int]string{0: "python writes, native reads", 1: "native writes, python reads"}[writer]
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			addRepo(w, "o/A", "1.0.0", "1.1.0")
			w.declare(`{"A": {"github": "o/A", "version": "^1.0.0"}}`)
			first, second := w.sides[writer], w.sides[1-writer]
			out := w.run(first, step{wantExit: 0})
			if out.exit != 0 || out.tags["A"] != "1.1.0" {
				t.Fatalf("writer: %+v", out)
			}
			// the reader uses the writer's store, lock and nothing else; a newer release must not be seen
			w.mock.Repos["o/A"].Releases = append(w.mock.Repos["o/A"].Releases, w.mock.Repos["o/A"].Releases[0])
			w.mock.Repos["o/A"].Releases[2].Tag = "1.2.0"
			lock, _ := os.ReadFile(filepath.Join(first.project, "userPreferences.u", "dependencies-lock.json"))
			os.MkdirAll(filepath.Join(second.project, "userPreferences.u"), 0o755)
			os.WriteFile(filepath.Join(second.project, "userPreferences.u", "dependencies-lock.json"), lock, 0o644)
			second.store = first.store
			w.mock.ResetCalls()
			got := w.run(second, step{args: []string{"--offline"}})
			if got.exit != 0 || got.tags["A"] != "1.1.0" {
				t.Fatalf("reader (offline, pinned by the writer's lock): exit %d tags %v\n%s", got.exit, got.tags, got.stderr)
			}
			if len(got.calls) != 0 {
				t.Fatalf("offline made requests: %v", got.calls)
			}
		})
	}
}
