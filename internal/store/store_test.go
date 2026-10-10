package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aipm/internal/provider"
)

// macOS's /var temp prefix is itself a symlink. Resolve the test directory,
// not production targets, so tests exercise genuinely symlink-free paths.
func safeTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func fixture(t *testing.T) (*Store, string, Registry, Registry) {
	t.Helper()
	root := safeTemp(t)
	s := New(filepath.Join(root, "private"))
	path := filepath.Join(root, "client.json")
	before := emptyRegistry()
	next := emptyRegistry()
	next.Providers = []provider.Provider{{ID: strings.Repeat("a", 32), Tool: provider.Codex, Name: "test", BaseURL: "https://example.invalid", Model: "model", AuthMode: "token", Secret: "test-only-secret"}}
	next.Snapshots["codex"] = map[string]any{"value": 1, "nested": map[string]any{"ok": true}}
	return s, path, before, next
}
func put(t *testing.T, path string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, b, mode); err != nil {
		t.Fatal(err)
	}
}
func contents(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func permission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	i, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if i.Mode().Perm() != want {
		t.Fatalf("mode %s = %o want %o", path, i.Mode().Perm(), want)
	}
}
func equalRegistry(t *testing.T, got, want Registry) {
	t.Helper()
	a, e := canonical(got)
	if e != nil {
		t.Fatal(e)
	}
	b, e := canonical(want)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("registry mismatch")
	}
}

func TestLoadReadOnly(t *testing.T) {
	s, _, empty, _ := fixture(t)
	r, e := s.Load()
	if e != nil {
		t.Fatal(e)
	}
	equalRegistry(t, r, empty)
	if s.Pending() {
		t.Fatal("pending absent store")
	}
	if e = s.Recover(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(s.Dir); !os.IsNotExist(e) {
		t.Fatal("read created store")
	}
	if e = os.Mkdir(s.Dir, 0700); e != nil {
		t.Fatal(e)
	}
	put(t, s.RegistryPath(), nil, 0644)
	r, e = s.Load()
	if e != nil {
		t.Fatal(e)
	}
	equalRegistry(t, r, empty)
	permission(t, s.RegistryPath(), 0644)
	entries, e := os.ReadDir(s.Dir)
	if e != nil || len(entries) != 1 {
		t.Fatal("load created files")
	}
}

func TestRegistryCRUDAndCAS(t *testing.T) {
	s, _, empty, next := fixture(t)
	if e := s.Save(empty, next); e != nil {
		t.Fatal(e)
	}
	permission(t, s.Dir, 0700)
	permission(t, s.RegistryPath(), 0600)
	permission(t, filepath.Join(s.Dir, ".lock"), 0600)
	r, e := s.Load()
	if e != nil {
		t.Fatal(e)
	}
	equalRegistry(t, r, next)
	raw := contents(t, s.RegistryPath())
	if e = s.Save(empty, empty); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale save: %v", e)
	}
	if !bytes.Equal(raw, contents(t, s.RegistryPath())) {
		t.Fatal("CAS changed registry")
	}
	// Whitespace is not part of semantic CAS. Snapshot ints roundtrip too.
	put(t, s.RegistryPath(), append([]byte("\n "), raw...), 0600)
	if e = s.Save(next, empty); e != nil {
		t.Fatal(e)
	}
	r, e = s.Load()
	if e != nil {
		t.Fatal(e)
	}
	equalRegistry(t, r, empty)
	if e = s.Save(empty, next); e != nil {
		t.Fatal(e)
	}
	invalid := next
	invalid.Version = 2
	if e = s.Save(next, invalid); e == nil {
		t.Fatal("invalid next accepted")
	}
	invalid = next
	invalid.Providers = append(append([]provider.Provider{}, next.Providers...), next.Providers[0])
	if e = s.Save(next, invalid); e == nil {
		t.Fatal("duplicate providers accepted")
	}
}

func TestStrictJSON(t *testing.T) {
	s, _, _, next := fixture(t)
	if e := os.Mkdir(s.Dir, 0700); e != nil {
		t.Fatal(e)
	}
	valid, e := canonical(next)
	if e != nil {
		t.Fatal(e)
	}
	cases := []string{
		`null`, `{}`, `[]`, `{"version":2,"providers":[],"snapshots":{}}`,
		`{"version":1,"version":1,"providers":[],"snapshots":{}}`,
		`{"version":1,"providers":null,"snapshots":{}}`,
		`{"version":1,"providers":[],"snapshots":{"x":null}}`,
		`{"version":1.0,"providers":[],"snapshots":{}}`,
		`{"version":"secret-input","providers":[],"snapshots":{}}`,
		`{"version":1,"providers":[],"snapshots":{},"secret-input":1}`,
		`{"Version":1,"providers":[],"snapshots":{}}`,
		`{"version":1,"providers":[],"snapshots":{}} true`,
		`{"version":1,"providers":[],"snapshots":{"x":{"secret-input":1,"secret-input":2}}}`,
		strings.Replace(string(valid), `"name":"test"`, `"name":"test","name":"secret-input"`, 1),
		strings.Replace(string(valid), `"name":"test"`, `"name":3`, 1),
		strings.Replace(string(valid), `"model":"model"`, `"model":null`, 1),
		strings.Replace(string(valid), `"base_url":"https://example.invalid"`, `"base_url":"invalid-secret-input"`, 1),
		strings.Repeat(" ", maxBytes+1),
	}
	for i, input := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			put(t, s.RegistryPath(), []byte(input), 0600)
			_, e := s.Load()
			if e == nil {
				t.Fatal("invalid JSON accepted")
			}
			if strings.Contains(e.Error(), "secret-input") || strings.Contains(e.Error(), "test-only-secret") {
				t.Fatal("sensitive error")
			}
		})
	}
}

func TestApplyAndModes(t *testing.T) {
	for _, existed := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existed], func(t *testing.T) {
			s, path, empty, next := fixture(t)
			var before []byte
			mode := os.FileMode(0600)
			if existed {
				before = []byte("before")
				mode = 0644
				put(t, path, before, mode)
			}
			backup, e := s.Apply(path, before, existed, []byte("after"), empty, next)
			if e != nil {
				t.Fatal(e)
			}
			if string(contents(t, path)) != "after" || s.Pending() {
				t.Fatal("apply incomplete")
			}
			permission(t, path, 0600)
			if existed {
				if filepath.Dir(backup) != filepath.Dir(path) || !bytes.Equal(contents(t, backup), before) {
					t.Fatal("bad backup")
				}
				permission(t, backup, 0600)
			} else if backup != "" {
				t.Fatal("backup for absent file")
			}
			r, e := s.Load()
			if e != nil {
				t.Fatal(e)
			}
			equalRegistry(t, r, next)
		})
	}
}

func TestApplyFailureRollback(t *testing.T) {
	phases := []string{"before-journal", "after-journal", "after-client", "before-registry", "after-registry", "before-cleanup"}
	for _, phase := range phases {
		for _, existed := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "-new", true: "-existing"}[existed], func(t *testing.T) {
				s, path, empty, next := fixture(t)
				var before []byte
				if existed {
					before = []byte("before")
					put(t, path, before, 0640)
				}
				injected := errors.New("injected")
				s.hook = func(p string) error {
					if p == phase {
						return injected
					}
					return nil
				}
				_, e := s.Apply(path, before, existed, []byte("after"), empty, next)
				if !errors.Is(e, injected) {
					t.Fatalf("not injected: %v", e)
				}
				s.hook = nil
				committed := phase == "after-registry" || phase == "before-cleanup"
				got, exists, e := ReadTarget(path)
				if e != nil {
					t.Fatal(e)
				}
				if committed {
					if !exists || string(got) != "after" || !s.Pending() {
						t.Fatal("committed transaction lost")
					}
					if e = s.Recover(); e != nil {
						t.Fatal(e)
					}
				} else if exists != existed || !bytes.Equal(got, before) || s.Pending() {
					t.Fatal("rollback failed")
				}
				r, e := s.Load()
				if e != nil {
					t.Fatal(e)
				}
				if committed {
					equalRegistry(t, r, next)
				} else {
					equalRegistry(t, r, empty)
				}
				if existed {
					if committed {
						permission(t, path, 0600)
					} else {
						permission(t, path, 0640)
					}
				}
			})
		}
	}
}

func crash(t *testing.T, s *Store, path string, before []byte, existed bool, empty, next Registry, phase string) {
	t.Helper()
	marker := &struct{}{}
	s.hook = func(p string) error {
		if p == phase {
			panic(marker)
		}
		return nil
	}
	func() {
		defer func() {
			if recover() != marker {
				t.Fatal("crash hook not reached")
			}
		}()
		_, _ = s.Apply(path, before, existed, []byte("after"), empty, next)
	}()
	s.hook = nil
}

func TestCrashRecovery(t *testing.T) {
	for _, phase := range []string{"after-journal", "after-client", "after-registry", "before-cleanup"} {
		for _, existed := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "-new", true: "-existing"}[existed], func(t *testing.T) {
				s, path, empty, next := fixture(t)
				var before []byte
				if existed {
					before = []byte("before")
					put(t, path, before, 0640)
				}
				crash(t, s, path, before, existed, empty, next, phase)
				recovered := New(s.Dir)
				if !recovered.Pending() {
					t.Fatal("no journal")
				}
				if e := recovered.Save(empty, next); !errors.Is(e, ErrPending) {
					t.Fatalf("save pending: %v", e)
				}
				if _, e := recovered.Apply(path, before, existed, []byte("other"), empty, next); !errors.Is(e, ErrPending) {
					t.Fatalf("apply pending: %v", e)
				}
				if e := recovered.Recover(); e != nil {
					t.Fatal(e)
				}
				if recovered.Pending() {
					t.Fatal("journal retained")
				}
				if e := recovered.Recover(); e != nil {
					t.Fatal(e)
				}
				r, e := recovered.Load()
				if e != nil {
					t.Fatal(e)
				}
				if phase == "after-journal" {
					equalRegistry(t, r, empty)
				} else {
					equalRegistry(t, r, next)
				}
			})
		}
	}
}

func TestExternalModification(t *testing.T) {
	for _, phase := range []string{"after-journal", "after-client", "before-registry"} {
		t.Run(phase, func(t *testing.T) {
			s, path, empty, next := fixture(t)
			put(t, path, []byte("before"), 0600)
			s.hook = func(p string) error {
				if p == phase {
					put(t, path, []byte("external"), 0600)
					return errors.New("injected")
				}
				return nil
			}
			_, e := s.Apply(path, []byte("before"), true, []byte("after"), empty, next)
			if e == nil || string(contents(t, path)) != "external" {
				t.Fatal("external config overwritten")
			}
			if !s.Pending() {
				t.Fatal("conflicting journal discarded")
			}
			if e = s.Recover(); e == nil || !s.Pending() {
				t.Fatal("external recovery accepted")
			}
		})
	}
	t.Run("registry-during-apply", func(t *testing.T) {
		s, path, empty, next := fixture(t)
		put(t, path, []byte("before"), 0600)
		external := emptyRegistry()
		external.Snapshots["x"] = map[string]any{"other": true}
		data, e := canonical(external)
		if e != nil {
			t.Fatal(e)
		}
		s.hook = func(p string) error {
			if p == "before-registry" {
				put(t, s.RegistryPath(), data, 0600)
			}
			return nil
		}
		_, e = s.Apply(path, []byte("before"), true, []byte("after"), empty, next)
		if e == nil || !bytes.Equal(contents(t, s.RegistryPath()), data) || !s.Pending() {
			t.Fatal("registry conflict not preserved")
		}
		if e = s.Recover(); !errors.Is(e, ErrConflict) {
			t.Fatalf("recover: %v", e)
		}
		if string(contents(t, path)) != "after" {
			t.Fatal("rollback clobbered committed client after registry conflict")
		}
	})
	for _, phase := range []string{"after-journal", "after-client", "after-registry"} {
		t.Run("recovery-registry-"+phase, func(t *testing.T) {
			s, path, empty, next := fixture(t)
			put(t, path, []byte("before"), 0600)
			crash(t, s, path, []byte("before"), true, empty, next, phase)
			external := []byte(`{"version":1,"providers":[],"snapshots":{"external":{"value":true}}}`)
			put(t, s.RegistryPath(), external, 0600)
			if e := s.Recover(); !errors.Is(e, ErrConflict) || !s.Pending() {
				t.Fatalf("recover: %v", e)
			}
			if !bytes.Equal(contents(t, s.RegistryPath()), external) {
				t.Fatal("external registry overwritten")
			}
		})
	}
}

func TestTargetSafety(t *testing.T) {
	root := safeTemp(t)
	real := filepath.Join(root, "real")
	if e := os.Mkdir(real, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(real, "config")
	put(t, file, []byte("safe"), 0600)
	symlink := filepath.Join(root, "link")
	if e := os.Symlink(real, symlink); e != nil {
		t.Fatal(e)
	}
	leaf := filepath.Join(root, "leaf")
	if e := os.Symlink(file, leaf); e != nil {
		t.Fatal(e)
	}
	dangling := filepath.Join(root, "dangling")
	if e := os.Symlink(filepath.Join(root, "missing"), dangling); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{real, leaf, dangling, filepath.Join(symlink, "config"), filepath.Join(symlink, "missing")} {
		if _, _, e := ReadTarget(path); e == nil {
			t.Fatalf("unsafe accepted: %s", path)
		}
		s := New(filepath.Join(root, "store"))
		if _, e := s.Apply(path, []byte("safe"), true, []byte("new"), emptyRegistry(), emptyRegistry()); e == nil {
			t.Fatal("unsafe apply")
		}
	}
	if string(contents(t, file)) != "safe" {
		t.Fatal("symlink changed real file")
	}
	missing := filepath.Join(root, "absent", "config")
	if b, x, e := ReadTarget(missing); e != nil || x || b != nil {
		t.Fatal("missing target")
	}
	large := filepath.Join(root, "large")
	put(t, large, make([]byte, maxBytes+1), 0600)
	if _, _, e := ReadTarget(large); e == nil {
		t.Fatal("oversize accepted")
	}
	exact := filepath.Join(root, "exact")
	put(t, exact, make([]byte, maxBytes), 0600)
	if b, x, e := ReadTarget(exact); e != nil || !x || len(b) != maxBytes {
		t.Fatal("4 MiB rejected")
	}
}

func TestStoreSymlinksAndJournal(t *testing.T) {
	for _, name := range []string{"registry.json", "journal.json", ".lock"} {
		t.Run(name, func(t *testing.T) {
			s, path, empty, next := fixture(t)
			if e := os.Mkdir(s.Dir, 0700); e != nil {
				t.Fatal(e)
			}
			put(t, path, []byte("untouched"), 0644)
			if e := os.Symlink(path, filepath.Join(s.Dir, name)); e != nil {
				t.Fatal(e)
			}
			if e := s.Save(empty, next); e == nil {
				t.Fatal("unsafe store save")
			}
			if name == "registry.json" {
				if _, e := s.Load(); e == nil {
					t.Fatal("unsafe load")
				}
			}
			if name == "journal.json" {
				if !s.Pending() || s.Recover() == nil {
					t.Fatal("unsafe journal accepted")
				}
			}
			if string(contents(t, path)) != "untouched" {
				t.Fatal("store touched symlink destination")
			}
			permission(t, path, 0644)
		})
	}
	s, _, empty, next := fixture(t)
	if e := os.Mkdir(s.Dir, 0700); e != nil {
		t.Fatal(e)
	}
	put(t, s.journalPath(), []byte(`{"secret-input":null}`), 0600)
	if !s.Pending() || s.Recover() == nil {
		t.Fatal("invalid journal accepted")
	}
	if e := s.Save(empty, next); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
}

func TestExistenceCAS(t *testing.T) {
	s, path, empty, next := fixture(t)
	put(t, path, nil, 0600)
	if _, e := s.Apply(path, nil, false, []byte("after"), empty, next); e == nil {
		t.Fatal("empty file mistaken for absent")
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Apply(path, nil, true, []byte("after"), empty, next); e == nil {
		t.Fatal("absent file mistaken for empty")
	}
	put(t, path, []byte("before"), 0600)
	s.hook = func(p string) error {
		if p == "after-journal" {
			put(t, path, []byte("changed"), 0600)
		}
		return nil
	}
	if _, e := s.Apply(path, []byte("before"), true, []byte("after"), empty, next); e == nil {
		t.Fatal("last-minute change ignored")
	}
	if string(contents(t, path)) != "changed" {
		t.Fatal("last-minute change overwritten")
	}
}

func TestMissingClientParents(t *testing.T) {
	s, path, empty, next := fixture(t)
	root := filepath.Dir(path)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	rootMode := info.Mode().Perm()
	path = filepath.Join(root, "client", "nested", "config")
	if _, _, e := ReadTarget(path); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Apply(path, nil, false, []byte("after"), empty, next); e != nil {
		t.Fatal(e)
	}
	permission(t, filepath.Join(root, "client"), 0700)
	permission(t, filepath.Join(root, "client", "nested"), 0700)
	permission(t, path, 0600)
	permission(t, root, rootMode)
}

func TestNormalizedJournalRecovery(t *testing.T) {
	s, path, empty, _ := fixture(t)
	next := Registry{Version: 1}
	crash(t, s, path, nil, false, empty, next, "after-client")
	if e := New(s.Dir).Recover(); e != nil {
		t.Fatal(e)
	}
	r, e := s.Load()
	if e != nil {
		t.Fatal(e)
	}
	equalRegistry(t, r, empty)
}

func TestSemanticNumbers(t *testing.T) {
	s, _, empty, next := fixture(t)
	next.Snapshots["codex"]["value"] = 1.0
	if e := s.Save(empty, next); e != nil {
		t.Fatal(e)
	}
	data := contents(t, s.RegistryPath())
	put(t, s.RegistryPath(), bytes.Replace(data, []byte(`"value":1`), []byte(`"value":1.000e0`), 1), 0600)
	if e := s.Save(next, empty); e != nil {
		t.Fatal(e)
	}
}

func TestRollbackOnlyOwnWrite(t *testing.T) {
	s, path, empty, next := fixture(t)
	put(t, path, []byte("before"), 0600)
	s.hook = func(phase string) error {
		if phase == "after-journal" {
			put(t, path, []byte("after"), 0600)
			return errors.New("injected")
		}
		return nil
	}
	if _, e := s.Apply(path, []byte("before"), true, []byte("after"), empty, next); e == nil {
		t.Fatal("expected failure")
	}
	if string(contents(t, path)) != "after" {
		t.Fatal("rolled back somebody else's write")
	}
	if !s.Pending() {
		t.Fatal("lost journal")
	}
}

func TestConcurrentCAS(t *testing.T) {
	s, _, empty, next := fixture(t)
	const count = 12
	var wg sync.WaitGroup
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- New(s.Dir).Save(empty, next) }()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("successful writes %d want 1", success)
	}
}
