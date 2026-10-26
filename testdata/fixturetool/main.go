// Command fixturetool is the one child process hivedeck's tests launch. It stands in for the
// shell scripts and system binaries (`/bin/sh`, `/bin/pwd`, `true`) those tests used to need,
// so the same tests run on every platform hivedeck builds for — Windows included, where none
// of those exist.
//
// Invoked with a `fixture:` subcommand it plays that part. Invoked any other way — copied
// onto a PATH under another name and handed whatever arguments that name would receive — it
// exits 0, which is all a fake `open` or `tmux` has to do.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(0)
	}
	arguments := os.Args[2:]
	switch os.Args[1] {
	case "fixture:exit":
		os.Exit(exitCodeArgument(arguments, 0))
	case "fixture:sleep":
		time.Sleep(time.Duration(exitCodeArgument(arguments, 0)) * time.Millisecond)
	case "fixture:pwd":
		printWorkingDirectory()
	case "fixture:print-env":
		printEnvironmentVariable(arguments)
	case "fixture:status":
		reportStatusAndExit(arguments)
	case "fixture:status-stream":
		streamStatusUntilReleased(arguments)
	case "fixture:touch":
		touchMarkerFile(arguments)
	default:
		os.Exit(0)
	}
}

// touchMarkerFile takes `<path>`: it creates an empty file at path and exits 0, so a test can
// tell whether this invocation ran at all by the marker file's presence.
func touchMarkerFile(arguments []string) {
	if len(arguments) == 0 {
		fail("missing marker path argument")
	}
	file, err := os.Create(arguments[0])
	if err != nil {
		fail("%v", err)
	}
	file.Close()
}

func fail(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "fixturetool: "+format+"\n", values...)
	os.Exit(2)
}

func exitCodeArgument(arguments []string, index int) int {
	if len(arguments) <= index {
		fail("missing exit code argument")
	}
	exitCode, err := strconv.Atoi(arguments[index])
	if err != nil {
		fail("exit code %q is not a number", arguments[index])
	}
	return exitCode
}

func printWorkingDirectory() {
	workingDirectory, err := os.Getwd()
	if err != nil {
		fail("%v", err)
	}
	fmt.Println(workingDirectory)
}

// printEnvironmentVariable exits 1 when the variable is unset or empty, so a test can tell
// "the value reached the child" from "the child started" by the exit code alone.
func printEnvironmentVariable(arguments []string) {
	if len(arguments) == 0 {
		fail("missing variable name")
	}
	value := os.Getenv(arguments[0])
	if value == "" {
		os.Exit(1)
	}
	fmt.Print(value)
}

type statusEvent struct {
	Event    string `json:"event"`
	Artifact string `json:"artifact,omitempty"`
}

func appendStatusEvent(event statusEvent) {
	statusFile := os.Getenv("FORMIC_STATUS_FILE")
	if statusFile == "" {
		fail("FORMIC_STATUS_FILE is not set")
	}
	line, err := json.Marshal(event)
	if err != nil {
		fail("%v", err)
	}
	file, err := os.OpenFile(statusFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fail("%v", err)
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		fail("%v", err)
	}
}

// reportStatusAndExit takes `<exit-code> <event>`: one terminal event, then that exit code.
func reportStatusAndExit(arguments []string) {
	exitCode := exitCodeArgument(arguments, 0)
	if len(arguments) < 2 {
		fail("missing event argument")
	}
	appendStatusEvent(statusEvent{Event: arguments[1], Artifact: "artifact.html"})
	os.Exit(exitCode)
}

// streamStatusUntilReleased takes `<gate-file>`: it publishes two events, then stays alive
// until the test creates the gate file, so a test can observe the run while it is running.
func streamStatusUntilReleased(arguments []string) {
	if len(arguments) == 0 {
		fail("missing gate file argument")
	}
	appendStatusEvent(statusEvent{Event: "started"})
	appendStatusEvent(statusEvent{Event: "progress", Artifact: "evidence.html"})
	for {
		if _, err := os.Stat(arguments[0]); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	appendStatusEvent(statusEvent{Event: "ok", Artifact: "evidence.html"})
}
