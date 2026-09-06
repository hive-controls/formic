package pane

import "fmt"

// Scrollback is a pane's bounded history of completed lines. It is bounded twice — by line
// count and by total bytes — because either bound alone is escapable: a child emitting
// millions of empty lines defeats a byte cap slowly, and one emitting a few enormous lines
// defeats a line cap immediately.
//
// Overflow always drops the OLDEST line, never the newest: the reason a person is watching a
// pane is what the child is doing now.
type Scrollback struct {
	maximumLines int
	maximumBytes int
	lines        []string
	bytes        int
	dropped      int
}

func NewScrollback(maximumLines, maximumBytes int) *Scrollback {
	return &Scrollback{maximumLines: maximumLines, maximumBytes: maximumBytes}
}

// Append adds one completed line, dropping as many of the oldest lines as the caps require.
// One line is always kept, whatever its size: a pane showing nothing at all is worse than a
// pane showing one line over its byte cap, and dropping unconditionally would loop forever on
// a single line larger than the cap.
func (scrollback *Scrollback) Append(line string) {
	scrollback.lines = append(scrollback.lines, line)
	scrollback.bytes += len(line)
	for len(scrollback.lines) > 1 && scrollback.overCap() {
		scrollback.bytes -= len(scrollback.lines[0])
		scrollback.lines = scrollback.lines[1:]
		scrollback.dropped++
	}
}

func (scrollback *Scrollback) overCap() bool {
	return len(scrollback.lines) > scrollback.maximumLines || scrollback.bytes > scrollback.maximumBytes
}

// Lines returns a copy, so a caller rendering a frame cannot be handed a slice the reader
// goroutine is about to re-slice underneath it.
func (scrollback *Scrollback) Lines() []string {
	copied := make([]string, len(scrollback.lines))
	copy(copied, scrollback.lines)
	return copied
}

func (scrollback *Scrollback) Dropped() int {
	return scrollback.dropped
}

// Marker names what the pane is no longer showing. A bounded buffer with no marker is
// indistinguishable from a child that simply never printed those lines.
func (scrollback *Scrollback) Marker() string {
	if scrollback.dropped == 0 {
		return ""
	}
	if scrollback.dropped == 1 {
		return "↑ 1 earlier line dropped"
	}
	return fmt.Sprintf("↑ %d earlier lines dropped", scrollback.dropped)
}
