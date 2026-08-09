// Package config loads the CD211 bootstrap configuration from command-line
// flags. The binary reads no environment variables.
package config

import (
	"flag"
	"fmt"
	"os"

	"github.com/turygo/cd211/internal/settings"
)

// Config is the validated bootstrap configuration.
type Config struct {
	HTTPAddress  string
	DatabasePath string
}

// Parse parses and validates the bootstrap configuration from command-line
// arguments (excluding the program name). Unknown flags and trailing
// positional arguments are errors; the FlagSet prints its usage to stderr
// before returning. Passing -h or --help prints usage and returns
// flag.ErrHelp.
func Parse(args []string) (Config, error) {
	flags := flag.NewFlagSet("cd211", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	httpAddress := flags.String("http-address", ":8080", "HTTP listen address as [host]:port")
	databasePath := flags.String("database-path", "/data/cd211.sqlite", "SQLite database file on a host-local filesystem")

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected positional argument %q", flags.Arg(0))
	}

	address := *httpAddress
	if err := settings.ValidateAddress("--http-address", address, true); err != nil {
		return Config{}, err
	}

	path, err := settings.AbsolutePath("--database-path", *databasePath)
	if err != nil {
		return Config{}, err
	}

	return Config{HTTPAddress: address, DatabasePath: path}, nil
}
