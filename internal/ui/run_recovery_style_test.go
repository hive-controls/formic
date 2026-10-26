package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/hive-controls/formic/internal/discovery"
	"github.com/hive-controls/formic/internal/profile"
	"github.com/hive-controls/formic/internal/runs"
)

// TestRunListTickDoesNotRunAbandonedRunRecovery pins that the live-status tick reloads
// the run list but never runs the recovery pass — that happens at startup and when the run
// list is explicitly opened.
func TestRunListTickDoesNotRunAbandonedRunRecovery(t *testing.T) {
	store := runs.Store{StateDirectory: t.TempDir()}
	row, err := store.Begin("phone-lookup", profile.ModeForeground, "/repo", nil, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	row.SupervisorPID = 999999
	if err := store.Save(row); err != nil {
		t.Fatal(err)
	}
	model := launchModel(t, store, discovery.Tool{Name: "phone-lookup", Title: "Phone Lookup"})
	model.showRuns = true

	model = model.pollLiveRuns()
	afterTick, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterTick.Outcome != "running" {
		t.Fatalf("after a plain tick outcome = %q, want recovery left for startup / explicit refresh", afterTick.Outcome)
	}

	model.showRuns = false
	_ = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	afterRefresh, err := store.Load(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRefresh.Outcome != "aborted" {
		t.Fatalf("after opening the run list outcome = %q, want aborted", afterRefresh.Outcome)
	}
}

// TestAbortedOutcomeHasItsOwnStyle pins that "aborted" does not read as "ok".
func TestAbortedOutcomeHasItsOwnStyle(t *testing.T) {
	if outcomeStyle("aborted").GetForeground() == outcomeStyle("ok").GetForeground() {
		t.Fatalf("aborted foreground = ok foreground (%v); want a distinct style", outcomeStyle("aborted").GetForeground())
	}
}
