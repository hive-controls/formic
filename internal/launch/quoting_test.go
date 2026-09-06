package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The osascript routes are the only places hivedeck builds a SHELL STRING instead of passing
// argv. Every other terminal takes argv and cannot mis-split it; these can, and the failure is
// silent — a mis-quoted argument does not error, it runs a different command. The tests below
// therefore EXECUTE the string rather than pattern-matching it.

// riskyArguments is one table shared by every test here: the three hazards §7.2 names
// (embedded quotes, spaces, newlines) plus the shell metacharacters that would turn a
// mis-quoted argument into a second command rather than a wrong one.
var riskyArguments = []string{
	"plain",
	"with space",
	"quote's",
	`double"quote`,
	"line\nbreak",
	"semi;colon",
	"pipe|bar",
	"dollar$HOME",
	"back`tick`",
	"amp&ersand",
	"glob*star?",
	"paren(these)s",
	"back\\slash",
	"tab\there",
	"trailing ",
	"$(echo injected)",
	"'; touch /tmp/hivedeck-quoting-breach; '",
}

// TestShellCommandSurvivesARealShell is the load-bearing one: it runs the exact string
// shellCommand produces through /bin/sh and compares the argv the child actually received,
// byte for byte, against the argv that was asked for.
func TestShellCommandSurvivesARealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the osascript routes are macOS-only")
	}
	directory := t.TempDir()
	reporter := filepath.Join(directory, "report-argv")
	output := filepath.Join(directory, "argv.txt")
	writeArgvReporter(t, reporter, output)

	workingDirectory := filepath.Join(directory, "a directory with spaces")
	if err := os.MkdirAll(workingDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	wrapper := append([]string{reporter}, riskyArguments...)
	script := shellCommand(workingDirectory, wrapper)

	command := exec.Command("/bin/sh", "-c", script)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the generated shell string failed to run: %v\nscript: %s\noutput: %s", err, script, combined)
	}

	recorded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	// The reporter writes one NUL-terminated record per argument, so an argument containing a
	// newline (or a tab, or a trailing space) round-trips without the separator being ambiguous.
	got := strings.Split(strings.TrimSuffix(string(recorded), "\x00"), "\x00")
	if len(got) != len(riskyArguments)+1 {
		t.Fatalf("child received %d arguments, want %d (argv[0] plus %d):\n%q",
			len(got), len(riskyArguments)+1, len(riskyArguments), got)
	}
	if got[0] != reporter {
		t.Fatalf("child argv[0] = %q, want %q", got[0], reporter)
	}
	for index, want := range riskyArguments {
		if got[index+1] != want {
			t.Fatalf("argument %d: child received %q, want %q — the quoting helper altered it",
				index, got[index+1], want)
		}
	}
	if _, err := os.Stat("/tmp/hivedeck-quoting-breach"); err == nil {
		os.Remove("/tmp/hivedeck-quoting-breach")
		t.Fatal("a quoted argument escaped its quotes and executed as a command")
	}
	// The cd must have landed too, or the child ran in the wrong directory with the right argv.
	if directoryRecord, err := os.ReadFile(output + ".pwd"); err != nil || strings.TrimSpace(string(directoryRecord)) != workingDirectory {
		t.Fatalf("child cwd = %q (err %v), want %q", strings.TrimSpace(string(directoryRecord)), err, workingDirectory)
	}
}

func writeArgvReporter(t *testing.T, reporter, output string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"pwd > " + strconv.Quote(output+".pwd") + "\n" +
		": > " + strconv.Quote(output) + "\n" +
		"printf '%s\\0' \"$0\" >> " + strconv.Quote(output) + "\n" +
		"for argument in \"$@\"; do printf '%s\\0' \"$argument\" >> " + strconv.Quote(output) + "; done\n"
	if err := os.WriteFile(reporter, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestAppleScriptQuotingSurvivesOsascript closes the SECOND escaping layer. shellCommand's
// output is embedded into AppleScript source with strconv.Quote, which is Go's string literal
// syntax, not AppleScript's — the two agree on \" \\ \n \t and disagree elsewhere. This runs
// the real osascript and asserts the string it parses back is the string that went in.
func TestAppleScriptQuotingSurvivesOsascript(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript is macOS-only")
	}
	if _, err := exec.LookPath("osascript"); err != nil {
		t.Skip("osascript is not on PATH")
	}
	command := shellCommand("/repo with space", append([]string{"hivedeck", "run"}, riskyArguments...))
	// The same strconv.Quote call terminalScript and iTermScript make.
	output, err := exec.Command("osascript", "-e", "return "+strconv.Quote(command)).Output()
	if err != nil {
		t.Fatalf("osascript could not parse the quoted command: %v\nquoted: %s", err, strconv.Quote(command))
	}
	// osascript's `return` prints the value followed by a newline; an embedded newline in the
	// value is printed literally, so only the final one is the terminator.
	got := strings.TrimSuffix(string(output), "\n")
	if got != command {
		t.Fatalf("AppleScript round-trip altered the command.\n got: %q\nwant: %q", got, command)
	}
}

// TestGhosttyScriptTargetsATabAndFallsBackToAWindow pins the two things the live pass on
// Ghostty 1.3.1 established: the command must reach a SHELL (Ghostty splits its `command`
// property into argv, so `cd x && y` handed over directly would never be interpreted), and
// `new tab` must name a window — without `in <window>` it fails with AppleScript error -1708
// when the application has none.
func TestGhosttyScriptTargetsATabAndFallsBackToAWindow(t *testing.T) {
	script := ghosttyScript("/repo with space", []string{"hivedeck", "run", "quote's"})
	for _, want := range []string{
		`tell application "Ghostty"`,
		"new surface configuration",
		"set initial working directory of cfg to " + strconv.Quote("/repo with space"),
		"if (count of windows) is 0 then",
		"new window with configuration cfg",
		"new tab in window 1 with configuration cfg",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("ghosttyScript is missing %q:\n%s", want, script)
		}
	}
	// The command must be handed to a shell, as one quoted argument, using the shared helper.
	wantCommand := "/bin/sh -c " + quoteShellArgument(shellCommand("/repo with space", []string{"hivedeck", "run", "quote's"}))
	if !strings.Contains(script, strconv.Quote(wantCommand)) {
		t.Fatalf("ghosttyScript does not run the command through a shell with the shared quoting:\n%s", script)
	}
}

// TestOsascriptRoutesEmbedTheQuotedCommand pins BOTH osascript routes to the same helper, so a
// future edit cannot quietly give one of them its own quoting.
func TestOsascriptRoutesEmbedTheQuotedCommand(t *testing.T) {
	wrapper := []string{"hivedeck", "run", "quote's and spaces"}
	expected := strconv.Quote(shellCommand("/repo with space", wrapper))
	for name, script := range map[string]string{
		"terminal-app": terminalScript("/repo with space", wrapper),
		"iterm2":       iTermScript("/repo with space", wrapper),
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("%s script does not embed the quoted command:\n%s", name, script)
		}
	}
}
