package runs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hive-controls/hivedeck/internal/profile"
)

const (
	rowFileName    = "run.json"
	statusFileName = "status.ndjson"
)

type Row struct {
	ID   string             `json:"id"`
	Tool string             `json:"tool"`
	Mode profile.LaunchMode `json:"mode"`
	Cwd  string             `json:"cwd"`
	// Argv and Env are the fully resolved child command and environment: argv[0] plus its
	// arguments, and the complete environment (process env + profile env-by-name resolved +
	// HIVEDECK_STATUS_FILE/HIVEDECK_RUN_ID/HIVEDECK_STATE_DIR). A tab-route wrapper is handed
	// only this row's path and reconstructs the child entirely from these fields, because no
	// terminal's new-tab spawn form reliably transfers a launching process's environment into
	// the session it creates.
	Argv       []string   `json:"argv,omitempty"`
	Env        []string   `json:"env,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Artifact   string     `json:"artifact,omitempty"`
	// Note carries a diagnostic when the status file could not be fully read or scanned
	// (unreadable, a line over the scanner buffer, ...) — it never blocks recording the exit
	// code, so the run is never lost, only annotated with why its outcome fell back.
	Note string `json:"note,omitempty"`
}

type Store struct {
	StateDirectory string
}

type statusEvent struct {
	Event    string `json:"event"`
	Artifact string `json:"artifact"`
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var runSequence atomic.Uint64

func ValidID(runID string) bool {
	return runIDPattern.MatchString(runID)
}

func DefaultStore() (Store, error) {
	stateDirectory, err := DefaultStateDirectory()
	if err != nil {
		return Store{}, err
	}
	return Store{StateDirectory: stateDirectory}, nil
}

// DefaultStateDirectory resolves to an ABSOLUTE path exactly once, here, against hivedeck's
// own invocation cwd — never left relative for a downstream consumer to resolve later against
// whatever directory happens to be current at THAT point instead. RowPath (and, through it,
// the row path a tab-route wrapper is handed on argv) is built directly from StateDirectory,
// so a relative HIVEDECK_STATE_DIR would otherwise embed a relative path into that argv — one
// the wrapper, started fresh in the TOOL's own cwd rather than hivedeck's, could never resolve
// back to the right location.
func DefaultStateDirectory() (string, error) {
	if override := os.Getenv("HIVEDECK_STATE_DIR"); override != "" {
		return absoluteStateDirectory(override)
	}
	directory, err := stateDirectory(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return absoluteStateDirectory(directory)
}

func absoluteStateDirectory(directory string) (string, error) {
	if filepath.IsAbs(directory) {
		return directory, nil
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	return absolute, nil
}

func stateDirectory(goos string, getenv func(string) string, homeDirectory func() (string, error)) (string, error) {
	switch goos {
	case "windows":
		if root := getenv("LOCALAPPDATA"); root != "" {
			return filepath.Join(root, "hivedeck"), nil
		}
		return "", fmt.Errorf("LOCALAPPDATA is not set")
	case "linux":
		if root := getenv("XDG_STATE_HOME"); root != "" {
			return filepath.Join(root, "hivedeck"), nil
		}
	}
	home, err := homeDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "hivedeck"), nil
	}
	return filepath.Join(home, ".local", "state", "hivedeck"), nil
}

func (store Store) Begin(tool string, mode profile.LaunchMode, cwd string, argv, environment []string, startedAt time.Time) (Row, error) {
	row := Row{
		ID: newRunID(startedAt), Tool: tool, Mode: mode, Cwd: cwd,
		Argv: argv, Env: environment,
		StartedAt: startedAt.UTC(), Outcome: "running",
	}
	if err := store.Save(row); err != nil {
		return Row{}, err
	}
	return row, nil
}

func (store Store) Save(row Row) error {
	if !ValidID(row.ID) {
		return fmt.Errorf("invalid run id %q", row.ID)
	}
	directory := store.runDirectory(row.ID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	data, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run row: %w", err)
	}
	temporaryPath := filepath.Join(directory, rowFileName+".tmp")
	if err := os.WriteFile(temporaryPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write run row: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, rowFileName)); err != nil {
		return fmt.Errorf("publish run row: %w", err)
	}
	return nil
}

func (store Store) Load(runID string) (Row, error) {
	if !ValidID(runID) {
		return Row{}, fmt.Errorf("invalid run id %q", runID)
	}
	data, err := os.ReadFile(filepath.Join(store.runDirectory(runID), rowFileName))
	if err != nil {
		return Row{}, fmt.Errorf("read run row: %w", err)
	}
	var row Row
	if err := json.Unmarshal(data, &row); err != nil {
		return Row{}, fmt.Errorf("decode run row: %w", err)
	}
	return row, nil
}

func (store Store) Complete(runID string, exitCode int, finishedAt time.Time) (Row, error) {
	row, err := store.Load(runID)
	if err != nil {
		return Row{}, err
	}
	// Exit code and finish time are the mandatory fallback (§8) and are written to DISK here,
	// in their own Save, BEFORE the status file is even opened — not merely assigned in memory
	// ahead of one later write. If reading or scanning the status file panics, or otherwise
	// never returns, this row is already durable with a safe fallback outcome; nothing about
	// the status file may prevent the exit code from reaching disk.
	row.ExitCode, row.FinishedAt = &exitCode, timePointer(finishedAt.UTC())
	row.Outcome = fallbackOutcome(exitCode)
	if err := store.Save(row); err != nil {
		return Row{}, err
	}
	outcome, artifact, note := readStatusFile(store.StatusFile(runID), exitCode)
	row.Outcome, row.Artifact, row.Note = outcome, artifact, note
	if err := store.Save(row); err != nil {
		return Row{}, err
	}
	return row, nil
}

func (store Store) List() ([]Row, error) {
	paths, err := filepath.Glob(filepath.Join(store.StateDirectory, "runs", "*", rowFileName))
	if err != nil {
		return nil, fmt.Errorf("scan run rows: %w", err)
	}
	rows := make([]Row, 0, len(paths))
	for _, path := range paths {
		row, readErr := readRow(path)
		if readErr != nil {
			return nil, readErr
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].StartedAt.After(rows[right].StartedAt) })
	return rows, nil
}

func (store Store) StatusFile(runID string) string {
	return filepath.Join(store.runDirectory(runID), statusFileName)
}

// RowPath returns the on-disk path of a run's row file, so a tab-route wrapper can be
// handed a single self-sufficient argument that locates its row without depending on any
// environment variable (§ launch: new-tab wrapper contract).
func (store Store) RowPath(runID string) string {
	return filepath.Join(store.runDirectory(runID), rowFileName)
}

// LoadFromPath reads a run row directly from its file path and reconstructs the owning
// Store from that same path, with no dependency on HIVEDECK_STATE_DIR or any other
// environment variable. This is what lets a new-tab wrapper, started fresh by a terminal
// that dropped its parent's environment, still find and complete its own run.
func LoadFromPath(rowPath string) (Store, Row, error) {
	data, err := os.ReadFile(rowPath)
	if err != nil {
		return Store{}, Row{}, fmt.Errorf("read run row: %w", err)
	}
	var row Row
	if err := json.Unmarshal(data, &row); err != nil {
		return Store{}, Row{}, fmt.Errorf("decode run row: %w", err)
	}
	runsDirectory := filepath.Dir(filepath.Dir(rowPath))
	store := Store{StateDirectory: filepath.Dir(runsDirectory)}
	return store, row, nil
}

func (store Store) runDirectory(runID string) string {
	return filepath.Join(store.StateDirectory, "runs", runID)
}

// maxStatusLineLength raises the scanner buffer well past bufio's historical 64 KiB default
// (bufio.MaxScanTokenSize), so a single long NDJSON status line — a lengthy artifact path or
// message — is read and parsed rather than aborting the scan with bufio.ErrTooLong.
const maxStatusLineLength = 1 << 20 // 1 MiB

// openStatusFile is a seam over os.Open so a test can inject a hard failure (including a
// panic) into the status-file read path, to prove Complete already persisted the exit code to
// disk in its own Save before ever reaching this call.
var openStatusFile = os.Open

// readStatusFile NEVER returns an error: a read or scan problem degrades to the exit-code
// fallback outcome plus a diagnostic note, so Complete can always persist a finished row. A
// line that merely fails to parse as JSON (a stray non-JSON line, or one truncated mid-object
// by a killed writer) is tolerated silently, exactly as before — only a genuine read/scan
// failure produces a note.
func readStatusFile(path string, exitCode int) (outcome, artifact, note string) {
	file, err := openStatusFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fallbackOutcome(exitCode), "", ""
	}
	if err != nil {
		return fallbackOutcome(exitCode), "", fmt.Sprintf("status file unreadable: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStatusLineLength)
	for scanner.Scan() {
		var event statusEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Artifact != "" {
			artifact = event.Artifact
		}
		if terminalOutcome(event.Event) {
			outcome = event.Event
		}
	}
	if err := scanner.Err(); err != nil {
		note = fmt.Sprintf("status file scan stopped early: %v", err)
	}
	if outcome == "" {
		outcome = fallbackOutcome(exitCode)
	}
	return outcome, artifact, note
}

func fallbackOutcome(exitCode int) string {
	if exitCode == 0 {
		return "ok"
	}
	return "failed"
}

func terminalOutcome(event string) bool {
	return event == "ok" || event == "failed" || event == "needs-human"
}

func readRow(path string) (Row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Row{}, fmt.Errorf("read run row: %w", err)
	}
	var row Row
	if err := json.Unmarshal(data, &row); err != nil {
		return Row{}, fmt.Errorf("decode run row: %w", err)
	}
	return row, nil
}

func newRunID(startedAt time.Time) string {
	stamp := startedAt.UTC().Format("20060102T150405.000000000Z")
	sequence := strconv.FormatUint(runSequence.Add(1), 10)
	return strings.ReplaceAll(stamp, ":", "") + "-" + strconv.Itoa(os.Getpid()) + "-" + sequence
}

func timePointer(value time.Time) *time.Time {
	return &value
}
