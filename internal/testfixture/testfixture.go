// Package testfixture builds the one child process hivedeck's tests launch
// (testdata/fixturetool) and hands tests its path, so no test depends on a shell or a system
// binary that exists on only some of the platforms hivedeck builds for.
package testfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	builtPath string
	buildErr  error
)

// Tool returns the absolute path of the built fixture tool. It is built at most once per
// test process, into a directory keyed by the tool's own source and the toolchain, so a
// repeated run reuses the binary and a source change rebuilds it.
func Tool(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() { builtPath, buildErr = build() })
	if buildErr != nil {
		t.Fatalf("build the fixture tool: %v", buildErr)
	}
	return builtPath
}

// InstallAs copies the fixture tool into directory under name, as the executable a PATH
// lookup for that bare name finds on this platform, and returns the copy's path. Run under
// any name other than its own subcommands, the tool exits 0.
func InstallAs(t *testing.T, directory, name string) string {
	t.Helper()
	installedPath := filepath.Join(directory, name+executableSuffix())
	if err := copyFile(Tool(t), installedPath); err != nil {
		t.Fatalf("install the fixture tool as %s: %v", name, err)
	}
	return installedPath
}

func executableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func sourceDirectory() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate the testfixture package source")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "fixturetool"), nil
}

func build() (string, error) {
	source, err := sourceDirectory()
	if err != nil {
		return "", err
	}
	sourceBytes, err := os.ReadFile(filepath.Join(source, "main.go"))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append(sourceBytes, []byte(runtime.Version()+runtime.GOOS+runtime.GOARCH)...))
	cacheDirectory := filepath.Join(os.TempDir(), "hivedeck-testfixture", hex.EncodeToString(digest[:8]))
	toolPath := filepath.Join(cacheDirectory, "fixturetool"+executableSuffix())
	if _, err := os.Stat(toolPath); err == nil {
		return toolPath, nil
	}
	if err := os.MkdirAll(cacheDirectory, 0o755); err != nil {
		return "", err
	}
	// Built beside its final name and renamed into place, so two test processes building at
	// once never observe a half-written executable.
	temporaryFile, err := os.CreateTemp(cacheDirectory, "building-*"+executableSuffix())
	if err != nil {
		return "", err
	}
	temporaryPath := temporaryFile.Name()
	temporaryFile.Close()
	defer os.Remove(temporaryPath)
	command := exec.Command("go", "build", "-o", temporaryPath, ".")
	command.Dir = source
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %v\n%s", err, output)
	}
	if err := os.Rename(temporaryPath, toolPath); err != nil {
		if _, statErr := os.Stat(toolPath); statErr == nil {
			return toolPath, nil
		}
		return "", err
	}
	return toolPath, nil
}

func copyFile(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}
