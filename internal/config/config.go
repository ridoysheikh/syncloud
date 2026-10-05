// Package config loads controller settings from flags and SYNCLOUD_* environment variables.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type Controller struct {
	// DataDir holds the SQLite database and all controller state.
	DataDir string
	// Listen is the address the API and dashboard listen on.
	Listen string
	// Dev enables development behavior (verbose logs, relaxed origin checks for the Vite dev server).
	Dev bool
}

func (c Controller) DBPath() string { return filepath.Join(c.DataDir, "syncloud.db") }

// LoadController parses args (without the program name). Environment variables
// provide defaults; flags override them.
func LoadController(args []string) (Controller, error) {
	fs := flag.NewFlagSet("syncloud-controller", flag.ContinueOnError)
	c := Controller{}
	fs.StringVar(&c.DataDir, "data-dir", env("SYNCLOUD_DATA_DIR", "/var/lib/syncloud"), "directory for controller state")
	fs.StringVar(&c.Listen, "listen", env("SYNCLOUD_LISTEN", "127.0.0.1:7070"), "API and dashboard listen address")
	fs.BoolVar(&c.Dev, "dev", env("SYNCLOUD_DEV", "") == "1", "development mode")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.DataDir == "" {
		return c, fmt.Errorf("data-dir must not be empty")
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
