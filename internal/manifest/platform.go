package manifest

import "github.com/hive-controls/formic/internal/formats"

// applyPlatformOverride folds the launch and health override declared for operatingSystem (a
// GOOS value: darwin, linux or windows) into the base launch and health, then clears every
// override block. LoadFile runs it once, against the host, so every later reader — launch
// resolution, actions, health checks, describe — sees one effective command and never has to
// know overrides exist. Each key an override sets replaces the base key; a key it omits keeps
// the base value.
func applyPlatformOverride(tool *formats.ToolManifest, operatingSystem string) {
	if launchOverride := launchOverrideFor(tool.Launch, operatingSystem); launchOverride != nil {
		if launchOverride.Command != nil {
			tool.Launch.Command = *launchOverride.Command
		}
		if launchOverride.Args != nil {
			tool.Launch.Args = launchOverride.Args
		}
		if launchOverride.Cwd != nil {
			tool.Launch.Cwd = launchOverride.Cwd
		}
	}
	tool.Launch.Darwin, tool.Launch.Linux, tool.Launch.Windows = nil, nil, nil
	if tool.Health == nil {
		return
	}
	if healthOverride := healthOverrideFor(*tool.Health, operatingSystem); healthOverride != nil {
		tool.Health.Check = healthOverride.Check
	}
	tool.Health.Darwin, tool.Health.Linux, tool.Health.Windows = nil, nil, nil
}

func launchOverrideFor(launch formats.ToolManifestLaunch, operatingSystem string) *formats.LaunchOverride {
	switch operatingSystem {
	case "darwin":
		return launch.Darwin
	case "linux":
		return launch.Linux
	case "windows":
		return launch.Windows
	}
	return nil
}

func healthOverrideFor(health formats.ToolManifestHealth, operatingSystem string) *formats.HealthOverride {
	switch operatingSystem {
	case "darwin":
		return health.Darwin
	case "linux":
		return health.Linux
	case "windows":
		return health.Windows
	}
	return nil
}
