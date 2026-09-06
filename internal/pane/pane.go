package pane

import (
	"io"
	"os/exec"
	"sync"
	"time"
)

// Defaults for a pane's bounds. They are deliberately modest: a pane is a window onto what a
// child is doing now, and the run's full output belongs in the tool's own log or artifact.
const (
	DefaultScrollbackLines = 2000
	DefaultScrollbackBytes = 1 << 20
	DefaultColumns         = 80
	DefaultKillGrace       = 2 * time.Second
)

const readBufferSize = 32 * 1024

// Options bound one pane. Every field has a default, so a caller that does not care about
// bounds still gets bounded ones.
type Options struct {
	ScrollbackLines int
	ScrollbackBytes int
	Columns         int
	KillGrace       time.Duration
}

func (options Options) withDefaults() Options {
	if options.ScrollbackLines <= 0 {
		options.ScrollbackLines = DefaultScrollbackLines
	}
	if options.ScrollbackBytes <= 0 {
		options.ScrollbackBytes = DefaultScrollbackBytes
	}
	if options.Columns <= 0 {
		options.Columns = DefaultColumns
	}
	if options.KillGrace <= 0 {
		options.KillGrace = DefaultKillGrace
	}
	return options
}

// Snapshot is everything a frame needs from one pane, taken under the pane's lock so a render
// can never observe a half-updated pane.
type Snapshot struct {
	RunID      string
	Title      string
	Lines      []string
	Marker     string
	Resets     int
	Running    bool
	Stopped    bool
	Failure    string
	Bytes      int64
	Generation uint64
}

// Pane is one child's captured output. Its reader goroutine writes only into the fields below,
// under the mutex, and NEVER produces a message for the UI: that is what lets the frame above
// it repaint on its own schedule no matter how fast the child writes.
type Pane struct {
	RunID   string
	Title   string
	options Options

	mutex      sync.Mutex
	screen     *Screen
	scrollback *Scrollback
	bytes      int64
	generation uint64
	running    bool
	stopped    bool
	waitError  error

	session *Session
	done    chan struct{}
}

// New builds a pane with no child attached. Consume gives it output; Open does both at once.
func New(runID, title string, options Options) *Pane {
	options = options.withDefaults()
	return &Pane{
		RunID: runID, Title: title, options: options,
		screen:     NewScreen(options.Columns),
		scrollback: NewScrollback(options.ScrollbackLines, options.ScrollbackBytes),
		running:    true,
		done:       make(chan struct{}),
	}
}

// Open starts command on its own pseudo-terminal and reads it into a new pane.
func Open(runID, title string, command *exec.Cmd, size Size, options Options) (*Pane, error) {
	options = options.withDefaults()
	options.Columns = int(size.Columns)
	session, err := StartSession(command, size)
	if err != nil {
		return nil, err
	}
	openedPane := New(runID, title, options)
	openedPane.session = session
	go openedPane.readUntilExit()
	return openedPane, nil
}

func (openedPane *Pane) readUntilExit() {
	openedPane.Consume(openedPane.session.Output())
	openedPane.finish(openedPane.session.Wait())
}

// Consume reads output until the reader ends. It blocks, so a caller with a live child runs it
// on its own goroutine; a test can call it directly with a fixed reader.
func (openedPane *Pane) Consume(reader io.Reader) {
	buffer := make([]byte, readBufferSize)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			openedPane.absorb(buffer[:count])
		}
		if err != nil {
			return
		}
	}
}

func (openedPane *Pane) absorb(data []byte) {
	openedPane.mutex.Lock()
	defer openedPane.mutex.Unlock()
	for _, line := range openedPane.screen.Write(data) {
		openedPane.scrollback.Append(line)
	}
	openedPane.bytes += int64(len(data))
	openedPane.generation++
}

func (openedPane *Pane) finish(waitError error) {
	openedPane.mutex.Lock()
	openedPane.running, openedPane.waitError = false, waitError
	openedPane.generation++
	openedPane.mutex.Unlock()
	close(openedPane.done)
}

// WaitError is how the child ended, left in the shape os/exec reported it so the launch seam
// can turn it into a run row's exit code with the one function that already does that for
// every other route.
func (openedPane *Pane) WaitError() error {
	<-openedPane.done
	openedPane.mutex.Lock()
	defer openedPane.mutex.Unlock()
	return openedPane.waitError
}

// Snapshot copies the pane's current state for one frame.
func (openedPane *Pane) Snapshot() Snapshot {
	openedPane.mutex.Lock()
	defer openedPane.mutex.Unlock()
	lines := openedPane.scrollback.Lines()
	if row := openedPane.screen.Row(); row != "" {
		lines = append(lines, row)
	}
	return Snapshot{
		RunID: openedPane.RunID, Title: openedPane.Title, Lines: lines,
		Marker: openedPane.scrollback.Marker(), Resets: openedPane.screen.Resets(),
		Running: openedPane.running, Stopped: openedPane.stopped,
		Failure: failureText(openedPane.waitError),
		Bytes:   openedPane.bytes, Generation: openedPane.generation,
	}
}

// Resize re-widths the decoder and the child's pseudo-terminal together. Doing only the first
// would wrap lines at a width the child has never heard of.
func (openedPane *Pane) Resize(size Size) error {
	openedPane.mutex.Lock()
	openedPane.screen.SetColumns(int(size.Columns))
	openedPane.mutex.Unlock()
	if openedPane.session == nil {
		return nil
	}
	return openedPane.session.Resize(size)
}

// Terminate is the explicit two-stage kill, with the pane's configured grace. It records that
// the stop was ASKED FOR, so a pane a person stopped on purpose is not reported afterwards as
// a tool that failed — the signal that ends it is indistinguishable from any other otherwise.
func (openedPane *Pane) Terminate() error {
	openedPane.mutex.Lock()
	openedPane.stopped = true
	openedPane.generation++
	openedPane.mutex.Unlock()
	if openedPane.session == nil {
		return nil
	}
	return openedPane.session.Terminate(openedPane.options.KillGrace)
}

// Close releases the pane's pseudo-terminal, unblocking a reader whose child left a grandchild
// holding the other side open.
func (openedPane *Pane) Close() error {
	if openedPane.session == nil {
		return nil
	}
	return openedPane.session.Close()
}

// Done is closed once the child has exited and its outcome has been recorded.
func (openedPane *Pane) Done() <-chan struct{} {
	return openedPane.done
}

func failureText(waitError error) string {
	if waitError == nil {
		return ""
	}
	return waitError.Error()
}
