// Package settings owns the durable CD211 runtime configuration persisted as
// key-value settings and loaded once setup has completed.
package settings

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Settings keys.
const (
	KeyCD2Address       = "cd2.address"
	KeyCD2Username      = "cd2.username"
	KeyCD2Password      = "cd2.password"
	KeyCD2Insecure      = "cd2.insecure"
	KeyCloudRoot        = "paths.cloud_root"
	KeyLocalRoot        = "paths.local_root"
	KeyOfflineTimeout   = "timeouts.offline"
	KeyCopyTimeout      = "timeouts.copy"
	KeyVerifyTimeout    = "timeouts.verify"
	KeySetupCompletedAt = "setup.completed_at"
)

// Config is the validated runtime configuration persisted as settings.
type Config struct {
	CD2Address     string
	CD2Username    string
	CD2Password    string
	CD2Insecure    bool
	CloudRoot      string
	LocalRoot      string
	OfflineTimeout time.Duration
	CopyTimeout    time.Duration
	VerifyTimeout  time.Duration
}

// Store persists the settings key-value table.
type Store interface {
	ListSettings(ctx context.Context) (map[string]string, error)
}

// Load reports whether setup has completed and, when it has, returns the
// strictly parsed configuration. A missing setup.completed_at key means setup
// has not finished and yields (zero, false, nil); invalid persisted values
// yield an error rather than silently falling back to setup mode.
func Load(ctx context.Context, st Store) (Config, bool, error) {
	values, err := st.ListSettings(ctx)
	if err != nil {
		return Config{}, false, err
	}
	if _, ok := values[KeySetupCompletedAt]; !ok {
		return Config{}, false, nil
	}
	cfg, err := Parse(values)
	if err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

// Parse validates a settings map into a Config. All nine keys are required
// and the returned error names the first missing or invalid key; unknown keys
// are ignored.
func Parse(values map[string]string) (Config, error) {
	var cfg Config

	cd2Address, err := trimRequired(values, KeyCD2Address)
	if err != nil {
		return Config{}, err
	}
	if err := ValidateAddress(KeyCD2Address, cd2Address, false); err != nil {
		return Config{}, err
	}
	cfg.CD2Address = cd2Address

	if cfg.CD2Username, err = required(values, KeyCD2Username); err != nil {
		return Config{}, err
	}
	if cfg.CD2Password, err = required(values, KeyCD2Password); err != nil {
		return Config{}, err
	}
	if cfg.CD2Insecure, err = boolValue(values, KeyCD2Insecure); err != nil {
		return Config{}, err
	}

	cloudRoot, err := trimRequired(values, KeyCloudRoot)
	if err != nil {
		return Config{}, err
	}
	if cfg.CloudRoot, err = AbsolutePath(KeyCloudRoot, cloudRoot); err != nil {
		return Config{}, err
	}
	localRoot, err := trimRequired(values, KeyLocalRoot)
	if err != nil {
		return Config{}, err
	}
	if cfg.LocalRoot, err = AbsolutePath(KeyLocalRoot, localRoot); err != nil {
		return Config{}, err
	}

	if cfg.OfflineTimeout, err = durationValue(values, KeyOfflineTimeout); err != nil {
		return Config{}, err
	}
	if cfg.CopyTimeout, err = durationValue(values, KeyCopyTimeout); err != nil {
		return Config{}, err
	}
	if cfg.VerifyTimeout, err = durationValue(values, KeyVerifyTimeout); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Values serializes cfg into the canonical settings map, without
// setup.completed_at.
func Values(cfg Config) map[string]string {
	return map[string]string{
		KeyCD2Address:     cfg.CD2Address,
		KeyCD2Username:    cfg.CD2Username,
		KeyCD2Password:    cfg.CD2Password,
		KeyCD2Insecure:    strconv.FormatBool(cfg.CD2Insecure),
		KeyCloudRoot:      cfg.CloudRoot,
		KeyLocalRoot:      cfg.LocalRoot,
		KeyOfflineTimeout: cfg.OfflineTimeout.String(),
		KeyCopyTimeout:    cfg.CopyTimeout.String(),
		KeyVerifyTimeout:  cfg.VerifyTimeout.String(),
	}
}

// ValidateAddress reports whether address is a host:port pair whose port lies
// in 1..65535. When allowEmptyHost is false the host must be non-empty. The
// error names the setting being validated.
func ValidateAddress(name, address string, allowEmptyHost bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%s must be a host:port address", name)
	}
	if !allowEmptyHost && host == "" {
		return fmt.Errorf("%s must include a host", name)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%s must include a port from 1 to 65535", name)
	}
	return nil
}

// AbsolutePath requires value to be an absolute path and returns its cleaned
// form. The error names the setting being validated.
func AbsolutePath(name, value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	return filepath.Clean(value), nil
}

func required(values map[string]string, key string) (string, error) {
	value, ok := values[key]
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func trimRequired(values map[string]string, key string) (string, error) {
	value, err := required(values, key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func durationValue(values map[string]string, key string) (time.Duration, error) {
	value, ok := values[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return duration, nil
}

func boolValue(values map[string]string, key string) (bool, error) {
	value, ok := values[key]
	if !ok {
		return false, fmt.Errorf("%s is required", key)
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return parsed, nil
}
