// Package version holds the build identity that bench records in
// environment.json and manifest.json.
package version

// Version is overridden at link time: -ldflags "-X github.com/servtuner/servtuner/bench/internal/version.Version=1.2.3".
var Version = "0.0.0-dev"
