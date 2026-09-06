package runs

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// Update is what a status file has said so far: the last event a tool reported, and the last
// artifact pointer it offered. Both are cumulative — an artifact reported at the start is
// still the artifact after ten progress events that mention none.
type Update struct {
	State    string
	Artifact string
}

// Monitor tails one run's status file. It is the ONLY way a run is watched live: no route
// parses the child's own output, so a tool's stderr stays human-readable diagnostics rather
// than a stream something has to pick JSON out of.
//
// It reads forward from a byte offset and holds back a trailing incomplete line, because a
// tool appending an event is not atomic: a poll that lands mid-write must see nothing rather
// than half an event.
type Monitor struct {
	path    string
	offset  int64
	partial []byte
	update  Update
	maximum int
}

// maximumPartialLine bounds the held-back fragment. A writer that never emits a newline would
// otherwise grow it without limit; past the bound the fragment is dropped and the monitor
// resynchronises on the next newline.
const maximumPartialLine = 1 << 20

func NewMonitor(path string) *Monitor {
	return &Monitor{path: path, maximum: maximumPartialLine}
}

// Poll reads whatever has been appended since the last call and reports the run's state,
// together with whether anything actually changed — so a caller can skip rewriting a row that
// nothing has moved. A missing or unreadable file is simply "nothing yet": the status file is
// optional, and the exit code remains the mandatory fallback.
func (monitor *Monitor) Poll() (Update, bool) {
	appended, ok := monitor.readAppended()
	if !ok || len(appended) == 0 {
		return monitor.update, false
	}
	previous := monitor.update
	monitor.consume(appended)
	return monitor.update, monitor.update != previous
}

func (monitor *Monitor) readAppended() ([]byte, bool) {
	file, err := os.Open(monitor.path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	if _, err := file.Seek(monitor.offset, 0); err != nil {
		return nil, false
	}
	appended, err := io.ReadAll(file)
	if err != nil {
		return nil, false
	}
	monitor.offset += int64(len(appended))
	return appended, true
}

func (monitor *Monitor) consume(appended []byte) {
	data := append(monitor.partial, appended...)
	for {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			break
		}
		monitor.apply(data[:newline])
		data = data[newline+1:]
	}
	if len(data) > monitor.maximum {
		// The fragment is past any plausible event: drop it and resynchronise, rather than
		// buffering a writer that will never terminate its line.
		data = nil
	}
	monitor.partial = append([]byte(nil), data...)
}

func (monitor *Monitor) apply(line []byte) {
	var event statusEvent
	// A line that does not parse is skipped in silence: a tool is free to be interrupted
	// mid-write, and a truncated object is not a reason to stop watching the run.
	if json.Unmarshal(line, &event) != nil {
		return
	}
	if event.Event != "" {
		monitor.update.State = event.Event
	}
	if event.Artifact != "" {
		monitor.update.Artifact = event.Artifact
	}
}

// UpdateLive records what a monitor has seen on a run that is still going. It NEVER touches a
// run that has already finished: the exit code and the terminal outcome are written once, by
// Complete, and a late-arriving status line must not reopen them.
func (store Store) UpdateLive(runID string, update Update) (Row, error) {
	row, err := store.Load(runID)
	if err != nil {
		return Row{}, err
	}
	if row.FinishedAt != nil {
		return row, nil
	}
	if update.State != "" {
		row.Outcome = update.State
	}
	if update.Artifact != "" {
		row.Artifact = update.Artifact
	}
	if err := store.Save(row); err != nil {
		return Row{}, err
	}
	return row, nil
}
