// Package config loads CD211 runtime configuration from the environment.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	usernameEnv       = "CD211_USERNAME"
	passwordEnv       = "CD211_PASSWORD"
	cd2AddressEnv     = "CD2_ADDRESS"
	cd2UsernameEnv    = "CD2_USERNAME"
	cd2PasswordEnv    = "CD2_PASSWORD"
	cd2InsecureEnv    = "CD2_INSECURE"
	httpAddressEnv    = "CD211_HTTP_ADDRESS"
	databasePathEnv   = "DATABASE_PATH"
	cloudRootEnv      = "CLOUD_ROOT"
	localRootEnv      = "LOCAL_ROOT"
	offlineTimeoutEnv = "CD211_OFFLINE_TIMEOUT"
	copyTimeoutEnv    = "CD211_COPY_TIMEOUT"
	verifyTimeoutEnv  = "CD211_VERIFY_TIMEOUT"
)

// Config is the validated runtime configuration.
type Config struct {
	Username       string
	Password       string
	CD2Address     string
	CD2Username    string
	CD2Password    string
	CD2Insecure    bool
	HTTPAddress    string
	DatabasePath   string
	CloudRoot      string
	LocalRoot      string
	OfflineTimeout time.Duration
	CopyTimeout    time.Duration
	VerifyTimeout  time.Duration
}

// Load reads and validates configuration from the process environment.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	username, err := required(lookup, usernameEnv)
	if err != nil {
		return Config{}, err
	}
	password, err := required(lookup, passwordEnv)
	if err != nil {
		return Config{}, err
	}
	cd2Address, err := required(lookup, cd2AddressEnv)
	if err != nil {
		return Config{}, err
	}
	cd2Username, err := required(lookup, cd2UsernameEnv)
	if err != nil {
		return Config{}, err
	}
	cd2Password, err := required(lookup, cd2PasswordEnv)
	if err != nil {
		return Config{}, err
	}

	cd2Address = strings.TrimSpace(cd2Address)
	if err := validateAddress(cd2AddressEnv, cd2Address, false); err != nil {
		return Config{}, err
	}
	cd2Insecure, err := boolOrDefault(lookup, cd2InsecureEnv, false)
	if err != nil {
		return Config{}, err
	}

	httpAddress := valueOrDefault(lookup, httpAddressEnv, ":8080")
	if err := validateAddress(httpAddressEnv, httpAddress, true); err != nil {
		return Config{}, err
	}

	databasePath, err := absolutePath(databasePathEnv, valueOrDefault(lookup, databasePathEnv, "/data/cd211.sqlite"))
	if err != nil {
		return Config{}, err
	}
	cloudRoot, err := absolutePath(cloudRootEnv, valueOrDefault(lookup, cloudRootEnv, "/115open/云下载"))
	if err != nil {
		return Config{}, err
	}
	localRoot, err := absolutePath(localRootEnv, valueOrDefault(lookup, localRootEnv, "/downloads"))
	if err != nil {
		return Config{}, err
	}
	offlineTimeout, err := durationOrDefault(lookup, offlineTimeoutEnv, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	copyTimeout, err := durationOrDefault(lookup, copyTimeoutEnv, 72*time.Hour)
	if err != nil {
		return Config{}, err
	}
	verifyTimeout, err := durationOrDefault(lookup, verifyTimeoutEnv, 10*time.Minute)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Username:       username,
		Password:       password,
		CD2Address:     cd2Address,
		CD2Username:    cd2Username,
		CD2Password:    cd2Password,
		CD2Insecure:    cd2Insecure,
		HTTPAddress:    httpAddress,
		DatabasePath:   databasePath,
		CloudRoot:      cloudRoot,
		LocalRoot:      localRoot,
		OfflineTimeout: offlineTimeout,
		CopyTimeout:    copyTimeout,
		VerifyTimeout:  verifyTimeout,
	}, nil
}

func required(lookup func(string) (string, bool), name string) (string, error) {
	value, ok := lookup(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func valueOrDefault(lookup func(string) (string, bool), name, fallback string) string {
	value, ok := lookup(name)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(value)
}

func durationOrDefault(lookup func(string) (string, bool), name string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(name)
	if !ok {
		return fallback, nil
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

func boolOrDefault(lookup func(string) (string, bool), name string, fallback bool) (bool, error) {
	value, ok := lookup(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return parsed, nil
}

func validateAddress(name, address string, allowEmptyHost bool) error {
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

func absolutePath(name, value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	return filepath.Clean(value), nil
}
