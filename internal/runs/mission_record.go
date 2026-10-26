package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// MissionRecord is the platform's own completion record for a mission run, written to
// <evidence folder>/mission-run.json (§9: "the folder holds only the platform's record" for a
// tool that declares no outputs.evidenceDir). It is built entirely from a Row (orchestrator
// ruling, car 10b E1: the record writer lives in internal/runs, the package both internal/launch
// and internal/mission already import, so every completion route — CLI, deck foreground, pane,
// and the tab/split wrapper — shares the one write through Row alone) — Inputs is already
// redacted on the row (Row.Inputs) before WriteMissionRecord ever sees it.
type MissionRecord struct {
	Mission     string            `json:"mission"`
	RunID       string            `json:"runId"`
	Tool        string            `json:"tool"`
	StartedAt   time.Time         `json:"startedAt"`
	FinishedAt  *time.Time        `json:"finishedAt,omitempty"`
	ExitCode    *int              `json:"exitCode,omitempty"`
	Outcome     string            `json:"outcome"`
	EvidenceDir string            `json:"evidenceDir,omitempty"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	// MissionFile names the discovered mission definition's own source file (car 10b E0 restore —
	// the pre-hoist mission.Record carried this; the orchestrator's E1 field list omitted it).
	MissionFile string `json:"missionFile,omitempty"`
}

// WriteMissionRecord writes row's own mission record to <folder>/mission-run.json — the one
// place this happens, shared by every completion route through launch.Finish/launch.Abandon.
func WriteMissionRecord(row Row, folder string) error {
	record := MissionRecord{
		Mission: row.Mission, RunID: row.ID, Tool: row.Tool,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, ExitCode: row.ExitCode,
		Outcome: row.Outcome, EvidenceDir: row.EvidenceDir, Inputs: row.Inputs,
		MissionFile: row.MissionPath,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(folder, "mission-run.json"), append(data, '\n'), 0o644)
}
