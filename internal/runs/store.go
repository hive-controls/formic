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

	"github.com/hive-controls/formic/internal/profile"
)

const (
	rowFileName    = "run.json"
	replayFileName = "replay.json"
	statusFileName = "status.ndjson"
)

type Row struct {
	ID   string             `json:"id"`
	Tool string             `json:"tool"`
	Mode profile.LaunchMode `json:"mode"`
	// Mission is the mission's name, set only when this run was launched through
	// `formic mission run` (§9: "the run row is an ordinary one that also records the
	// mission's name") — v1 missions have exactly one step, so no separate step field.
	Mission string `json:"mission,omitempty"`
	// MissionPath is the discovered mission definition's own file path (§9 "the run row is an
	// ordinary one that also records the mission's name"; restored car 10b E0 after E1's hoist
	// omitted it) — set only when Mission is set, and is what WriteMissionRecord's MissionFile
	// names the record's own source mission file from.
	MissionPath string `json:"missionPath,omitempty"`
	// Action is the action this run launched; empty for a manifest with no actions[] and for
	// every row written before actions existed.
	Action string `json:"action,omitempty"`
	// Cwd and Argv are the fully resolved child command and working directory, with every
	// sensitive input value substituted into them replaced by the literal "<redacted:NAME>"
	// (N10) — never the real value. The tab-route wrapper does NOT reconstruct the child from
	// this row: it reads the real values once from the ephemeral replay file beside it (see
	// WriteReplay/ReadAndDeleteReplay) and deletes that file before the child ever starts.
	Argv []string `json:"argv,omitempty"`
	Cwd  string   `json:"cwd"`
	// EnvNames is the child's resolved environment, by NAME ONLY — never a value. Holding the
	// full child environment in plaintext, including a needs-resolved secret, is the leak this
	// closes; the manifest format has no way to mark a declared env[] entry as non-sensitive,
	// so this is a blanket redaction, not a scoped one. The real values live
	// only in memory (for the foreground/pane routes, which run the child directly) or in the
	// ephemeral replay file (for the tab route).
	EnvNames []string `json:"envNames,omitempty"`
	// EvidenceDir is the manifest's outputs.evidenceDir resolved at launch time (a NON-secret
	// path, distinct from EnvNames' blanket name-only redaction), made absolute against Cwd —
	// what the run list falls back to showing/opening when the run's own status stream never
	// reported an artifact. Resolving and storing it here, once, at launch, is what lets the
	// deck keep this feature without reading any value out of the child's real environment.
	EvidenceDir string `json:"evidenceDir,omitempty"`
	// Inputs holds a mission run's own declared inputs, REDACTED (launch.RedactSensitiveInputValues)
	// before this row is ever saved — never a sensitive value — set only when Mission is set
	// (orchestrator ruling, car 10b E1). It is what WriteMissionRecord builds mission-run.json's
	// own inputs from, so the record reflects exactly what the row already durably holds, rather
	// than a value recomputed from data that may no longer be in memory by completion time.
	Inputs map[string]string `json:"inputs,omitempty"`
	// SupervisorPID is the process id of whichever process will actually call Complete on this
	// row — the TUI itself for foreground and pane rows, both begun and completed in the same
	// process. A tab row's real supervisor is the tab wrapper the terminal spawns, not the TUI
	// that recorded the row, so this field is not meaningful for tab rows and recovery never
	// reads it for one. Omitted (zero) on any row written before this field existed; such a row
	// is simply never recoverable, never misread as abandoned.
	SupervisorPID int `json:"supervisorPid,omitempty"`
	// SupervisorStartedAt is an opaque, platform-specific token identifying the supervisor
	// process INSTANCE named by SupervisorPID at the moment this row was written — never a
	// human-readable timestamp, just whatever processSupervisorAlive's platform half needs to
	// tell that exact instance apart from a different process a recycled pid may later name
	// (Windows recycles pids). Empty on any row written before this field existed, or on a
	// platform that does not implement the check (see liveness_unix.go) — such
	// a row falls back to a plain pid-existence check, never misread as abandoned just because
	// this field is absent.
	SupervisorStartedAt string     `json:"supervisorStartedAt,omitempty"`
	StartedAt           time.Time  `json:"startedAt"`
	FinishedAt          *time.Time `json:"finishedAt,omitempty"`
	ExitCode            *int       `json:"exitCode,omitempty"`
	Outcome             string     `json:"outcome,omitempty"`
	Artifact            string     `json:"artifact,omitempty"`
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

// DefaultStateDirectory resolves to an ABSOLUTE path exactly once, here, against formic's
// own invocation cwd — never left relative for a downstream consumer to resolve later against
// whatever directory happens to be current at THAT point instead. RowPath (and, through it,
// the row path a tab-route wrapper is handed on argv) is built directly from StateDirectory,
// so a relative FORMIC_STATE_DIR would otherwise embed a relative path into that argv — one
// the wrapper, started fresh in the TOOL's own cwd rather than formic's, could never resolve
// back to the right location.
func DefaultStateDirectory() (string, error) {
	if override := os.Getenv("FORMIC_STATE_DIR"); override != "" {
		return absoluteStateDirectory(override)
	}
	directory, err := stateDirectory(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	if err := migrateLegacyStateDirectory(directory); err != nil {
		return "", err
	}
	return absoluteStateDirectory(directory)
}

// migrateLegacyStateDirectory moves the pre-rename "hivedeck" run history (its state directory
// is formic's sibling on every platform) into formic's, once: only when formic has no runs
// directory yet. It moves runs/, not the whole directory, because on macOS formic's state
// directory is also its config directory and may already exist. A failed move is an error,
// never a silent fall-through to an empty history.
func migrateLegacyStateDirectory(directory string) error {
	runsDirectory := filepath.Join(directory, "runs")
	if _, err := os.Stat(runsDirectory); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	legacyDirectory := filepath.Join(filepath.Dir(directory), "hivedeck")
	legacyRunsDirectory := filepath.Join(legacyDirectory, "runs")
	if _, err := os.Stat(legacyRunsDirectory); err != nil {
		return nil
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory %s: %w", directory, err)
	}
	if err := os.Rename(legacyRunsDirectory, runsDirectory); err != nil {
		return fmt.Errorf("move run history from %s to %s: %w", legacyRunsDirectory, runsDirectory, err)
	}
	_ = os.Remove(legacyDirectory) // only succeeds once empty; anything else left there stays
	return nil
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
			return filepath.Join(root, "formic"), nil
		}
		return "", fmt.Errorf("LOCALAPPDATA is not set")
	case "linux":
		if root := getenv("XDG_STATE_HOME"); root != "" {
			return filepath.Join(root, "formic"), nil
		}
	}
	home, err := homeDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "formic"), nil
	}
	return filepath.Join(home, ".local", "state", "formic"), nil
}

// Begin writes the durable row: argv is the (already redacted, per caller) argv, and
// environmentNames is the environment's names only, never values (N10) — a caller with real
// values to persist for a separate process to replay writes them through WriteReplay instead,
// never here.
func (store Store) Begin(tool string, mode profile.LaunchMode, cwd string, argv, environmentNames []string, startedAt time.Time) (Row, error) {
	row := Row{
		ID: newRunID(startedAt), Tool: tool, Mode: mode, Cwd: cwd,
		Argv: argv, EnvNames: environmentNames, SupervisorPID: os.Getpid(),
		SupervisorStartedAt: supervisorStartedAt(),
		StartedAt:           startedAt.UTC(), Outcome: "running",
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
	if err := publishRename(temporaryPath, filepath.Join(directory, rowFileName)); err != nil {
		return fmt.Errorf("publish run row: %w", err)
	}
	return nil
}

// windowsRenamePublishRetryDelays bounds how long publishRename retries a Windows rename that
// failed with ERROR_ACCESS_DENIED — a same-process reader (store.Load/List, via os.ReadFile)
// can transiently hold the destination file open without FILE_SHARE_DELETE (Go's os.Open on
// Windows opens with FILE_SHARE_READ|FILE_SHARE_WRITE only), which makes MoveFileEx refuse to
// replace it. The reader's handle is open only for the duration of one
// os.ReadFile call, so a few short retries clear the race without masking a genuinely stuck
// handle (antivirus/indexer holding the file open indefinitely would still exhaust these and
// surface the error).
var windowsRenamePublishRetryDelays = []time.Duration{
	5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond,
}

// publishRename performs the temp-file-publish rename shared by Save and WriteReplay, retrying
// on Windows when the destination is transiently held open by a concurrent reader in this same
// process. Linux/macOS are untouched: os.Rename there does not fail on an open destination
// handle, so the first result is always returned as-is.
func publishRename(temporaryPath, destinationPath string) error {
	err := os.Rename(temporaryPath, destinationPath)
	if err == nil || runtime.GOOS != "windows" || !errors.Is(err, os.ErrPermission) {
		return err
	}
	for _, delay := range windowsRenamePublishRetryDelays {
		time.Sleep(delay)
		err = os.Rename(temporaryPath, destinationPath)
		if err == nil || !errors.Is(err, os.ErrPermission) {
			return err
		}
	}
	return err
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

// Abort completes a row whose LAUNCH itself never started the tool — a failed tab-route spawn
// (wt.exe/tmux/osascript itself erroring, never the tool) — without inventing an exit code or
// an ok/failed outcome the tool never produced. ExitCode stays nil; Outcome becomes "aborted",
// distinct from both "ok" and "failed" so a person reading the run list is never told the tool
// itself finished when it never ran; note is the only record of why.
func (store Store) Abort(runID, note string, finishedAt time.Time) (Row, error) {
	row, err := store.Load(runID)
	if err != nil {
		return Row{}, err
	}
	row.FinishedAt, row.Outcome, row.Note = timePointer(finishedAt.UTC()), "aborted", note
	if err := store.Save(row); err != nil {
		return Row{}, err
	}
	return row, nil
}

// recoverableForegroundRows is the pure half of recovery: given rows already loaded and a
// liveness check, it decides which "running" foreground rows have been abandoned — their
// supervising process is gone — without touching disk itself, so this is tested on every
// platform without needing a real dead process to find. Only foreground rows are ever
// considered: a foreground row's supervisor is the same process that recorded it (see
// Row.SupervisorPID), while a tab row's real supervisor is the `formic __replay-row <row>` wrapper the
// terminal spawns later — recovering THAT correctly would need the wrapper's own pid recorded
// once it actually starts, which nothing here does, so a live tab run is never at risk of being
// wrongly recovered by staying out of scope entirely.
func recoverableForegroundRows(rows []Row, alive func(pid int, startedAt string) bool) []Row {
	var abandoned []Row
	for _, row := range rows {
		if (row.Mode == profile.ModeForeground || row.Mode == profile.ModeCaptured) && row.Outcome == "running" && row.SupervisorPID != 0 && !alive(row.SupervisorPID, row.SupervisorStartedAt) {
			abandoned = append(abandoned, row)
		}
	}
	return abandoned
}

// RecoverAbandonedForegroundRuns marks every abandoned foreground row (see
// recoverableForegroundRows) as finished, honestly: ExitCode stays nil — never an exit code the
// tool never produced — and Outcome becomes "aborted", distinct from ok/failed/running, with a
// Note naming the gone pid. Call sites: wherever the run list is loaded for display, and
// `doctor`.
func (store Store) RecoverAbandonedForegroundRuns(finishedAt time.Time) ([]Row, error) {
	return store.recoverAbandonedForegroundRunsWithLiveness(processSupervisorAlive, finishedAt)
}

func (store Store) recoverAbandonedForegroundRunsWithLiveness(alive func(pid int, startedAt string) bool, finishedAt time.Time) ([]Row, error) {
	rows, err := store.List()
	if err != nil {
		return nil, err
	}
	abandoned := recoverableForegroundRows(rows, alive)
	for index := range abandoned {
		row := abandoned[index]
		row.FinishedAt = timePointer(finishedAt.UTC())
		row.Outcome = "aborted"
		row.Note = fmt.Sprintf("recovered: its supervising process (pid %d) is gone", row.SupervisorPID)
		if err := store.Save(row); err != nil {
			return nil, err
		}
		abandoned[index] = row
	}
	return abandoned, nil
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
// Store from that same path, with no dependency on FORMIC_STATE_DIR or any other
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

// replayPayload is the ephemeral file's entire content: the REAL argv/cwd/env a tab-route
// wrapper needs to reconstruct the child, exactly what the durable row held before N10 —
// now living only here, for the shortest lifetime this design settles for (N10: "there is no
// owner-only file mode" on Windows; Node and Go both report 0o666 and the file is as readable
// as its directory — protected by location under the state dir, delete-on-read, and lifetime,
// not by a mode).
type replayPayload struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
	Env  []string `json:"env"`
}

// ReplayPath returns the on-disk path of a run's ephemeral replay file, mirroring RowPath, so
// RunWrapper can derive it from the row path alone — no second argument, no environment
// variable.
func (store Store) ReplayPath(runID string) string {
	return filepath.Join(store.runDirectory(runID), replayFileName)
}

// WriteReplay persists the real, unredacted argv/cwd/env for one run beside its durable row.
// Written ONLY on the tab route (ruling 3): the foreground and pane routes build and run the
// command directly, in the same process, and never need a separate process to rebuild it.
// Same temp-file + rename publish as Save.
func (store Store) WriteReplay(runID string, argv []string, cwd string, environment []string) error {
	if !ValidID(runID) {
		return fmt.Errorf("invalid run id %q", runID)
	}
	directory := store.runDirectory(runID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	data, err := json.MarshalIndent(replayPayload{Argv: argv, Cwd: cwd, Env: environment}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode replay file: %w", err)
	}
	temporaryPath := filepath.Join(directory, replayFileName+".tmp")
	if err := os.WriteFile(temporaryPath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write replay file: %w", err)
	}
	if err := publishRename(temporaryPath, filepath.Join(directory, replayFileName)); err != nil {
		return fmt.Errorf("publish replay file: %w", err)
	}
	return nil
}

// ReadAndDeleteReplay reads runID's ephemeral replay file and deletes it immediately after a
// successful read — delete-on-read, the shortest lifetime N10 settles for. A missing file
// returns an error satisfying errors.Is(err, os.ErrNotExist), so the wrapper can tell "already
// consumed or never written" apart from any other failure and record a structured abort rather
// than falling back to the (redacted) durable row.
func (store Store) ReadAndDeleteReplay(runID string) ([]string, string, []string, error) {
	path := store.ReplayPath(runID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", nil, err
	}
	if removeErr := os.Remove(path); removeErr != nil {
		return nil, "", nil, fmt.Errorf("delete replay file: %w", removeErr)
	}
	var payload replayPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, "", nil, fmt.Errorf("decode replay file: %w", err)
	}
	return payload.Argv, payload.Cwd, payload.Env, nil
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
