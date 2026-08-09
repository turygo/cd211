package config

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    Config
		wantErr string
	}{
		{
			name: "uses defaults",
			args: []string{},
			want: Config{
				HTTPAddress:  ":8080",
				DatabasePath: "/data/cd211.sqlite",
			},
		},
		{
			name: "accepts flags with equals form",
			args: []string{
				"--http-address=[::1]:8080",
				"--database-path=/var/lib/cd211/../cd211.sqlite",
			},
			want: Config{
				HTTPAddress:  "[::1]:8080",
				DatabasePath: "/var/lib/cd211.sqlite",
			},
		},
		{
			name: "accepts flags with space-separated values",
			args: []string{
				"--http-address", "127.0.0.1:9090",
				"--database-path", "/var/lib/cd211.sqlite",
			},
			want: Config{
				HTTPAddress:  "127.0.0.1:9090",
				DatabasePath: "/var/lib/cd211.sqlite",
			},
		},
		{
			name:    "rejects invalid HTTP port",
			args:    []string{"--http-address=:0"},
			wantErr: "--http-address",
		},
		{
			name:    "rejects address without port",
			args:    []string{"--http-address=localhost"},
			wantErr: "--http-address",
		},
		{
			name:    "rejects relative database path",
			args:    []string{"--database-path=state.sqlite"},
			wantErr: "--database-path",
		},
		{
			name:    "rejects unknown flag",
			args:    []string{"--unknown"},
			wantErr: "flag provided but not defined",
		},
		{
			name:    "rejects positional argument",
			args:    []string{"extra"},
			wantErr: "unexpected positional argument",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Parse(%q) error = %v, want error containing %q", test.args, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", test.args, err)
			}
			if got != test.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", test.args, got, test.want)
			}
		})
	}
}

func TestParseHelp(t *testing.T) {
	if _, err := Parse([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(-h) error = %v, want flag.ErrHelp", err)
	}
}
