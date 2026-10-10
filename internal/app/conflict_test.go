package app

import (
	"aipm/internal/provider"
	"testing"
)

func TestPreparedDeleteRejectsClientChange(t *testing.T) {
	for _, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			a := isolatedApp(t)
			p := testProvider(tool)
			saveProvider(t, a, p)
			action, err := a.PrepareDelete(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			switchProvider(t, a, tool, p.ID)
			if _, err = a.Execute(action); err == nil {
				t.Fatal("stale deletion accepted")
			}
			assertStatus(t, a, tool, "managed", p.ID)
		})
	}
}

func TestPreparedInactiveEditRejectsExternalClientChange(t *testing.T) {
	for _, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			a := isolatedApp(t)
			p := testProvider(tool)
			saveProvider(t, a, p)
			p.Name = "new name"
			action, err := a.PrepareSave(p)
			if err != nil {
				t.Fatal(err)
			}
			b := []byte("model_provider = \"external\"\n")
			if tool == provider.Claude {
				b = []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://external.example"}}`)
			}
			writeTarget(t, a, tool, b)
			if _, err = a.Execute(action); err == nil {
				t.Fatal("stale edit accepted")
			}
			r, err := a.Store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if r.Providers[0].Name == p.Name {
				t.Fatal("stale edit persisted")
			}
		})
	}
}
