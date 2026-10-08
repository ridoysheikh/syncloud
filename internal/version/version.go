// Package version holds build metadata, set at link time with -ldflags.
package version

var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
)

// Repository is the project's home: its GitHub releases are where installs
// and upgrades come from.
const Repository = "https://github.com/ridoysheikh/syncloud"
