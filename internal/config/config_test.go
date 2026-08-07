package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	base := map[string]string{
		usernameEnv:    " user ",
		passwordEnv:    " password ",
		cd2AddressEnv:  " cd2.example:443 ",
		cd2UsernameEnv: " cd2-user ",
		cd2PasswordEnv: " cd2-password ",
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name: "uses defaults and preserves credentials",
			env:  base,
			want: Config{
				Username:       " user ",
				Password:       " password ",
				CD2Address:     "cd2.example:443",
				CD2Username:    " cd2-user ",
				CD2Password:    " cd2-password ",
				HTTPAddress:    ":8080",
				DatabasePath:   "/data/cd211.sqlite",
				CloudRoot:      "/115open/云下载",
				LocalRoot:      "/downloads",
				OfflineTimeout: 24 * time.Hour,
				CopyTimeout:    72 * time.Hour,
				VerifyTimeout:  10 * time.Minute,
			},
		},
		{
			name: "accepts IPv6 and cleans absolute paths",
			env: mergeEnv(base, map[string]string{
				cd2AddressEnv:     " [2001:db8::1]:443 ",
				httpAddressEnv:    " [::1]:8080 ",
				cd2InsecureEnv:    " true ",
				databasePathEnv:   " /var/lib/cd211/../cd211.sqlite ",
				cloudRootEnv:      " /115open/./云下载/ ",
				localRootEnv:      " /downloads/complete/../ ",
				offlineTimeoutEnv: " 2h ",
				copyTimeoutEnv:    " 3h ",
				verifyTimeoutEnv:  " 4m ",
			}),
			want: Config{
				Username:       " user ",
				Password:       " password ",
				CD2Address:     "[2001:db8::1]:443",
				CD2Username:    " cd2-user ",
				CD2Insecure:    true,
				CD2Password:    " cd2-password ",
				HTTPAddress:    "[::1]:8080",
				DatabasePath:   "/var/lib/cd211.sqlite",
				CloudRoot:      "/115open/云下载",
				LocalRoot:      "/downloads",
				OfflineTimeout: 2 * time.Hour,
				CopyTimeout:    3 * time.Hour,
				VerifyTimeout:  4 * time.Minute,
			},
		},
		{
			name: "rejects relative local root",
			env: mergeEnv(base, map[string]string{
				localRootEnv: "downloads",
			}),
			wantErr: localRootEnv,
		},
		{
			name: "rejects invalid CD2 port",
			env: mergeEnv(base, map[string]string{
				cd2AddressEnv: "cd2.example:0",
			}),
			wantErr: cd2AddressEnv,
		},
		{
			name: "rejects invalid phase timeout",
			env: mergeEnv(base, map[string]string{
				copyTimeoutEnv: "0s",
			}),
			wantErr: copyTimeoutEnv,
		},
		{
			name: "rejects invalid insecure transport flag",
			env: mergeEnv(base, map[string]string{
				cd2InsecureEnv: "sometimes",
			}),
			wantErr: cd2InsecureEnv,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := load(mapLookup(test.env))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("load() error = %v, want error containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("load() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("load() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestLoadRejectsMissingCredentialsWithoutLeakingSecrets(t *testing.T) {
	secrets := []string{
		"username-secret",
		"password-secret",
		"cd2-username-secret",
		"cd2-password-secret",
	}
	base := map[string]string{
		usernameEnv:    secrets[0],
		passwordEnv:    secrets[1],
		cd2AddressEnv:  "cd2.example:443",
		cd2UsernameEnv: secrets[2],
		cd2PasswordEnv: secrets[3],
	}

	for _, missing := range []string{usernameEnv, passwordEnv, cd2AddressEnv, cd2UsernameEnv, cd2PasswordEnv} {
		t.Run(missing, func(t *testing.T) {
			env := mergeEnv(base, map[string]string{missing: " \t "})
			_, err := load(mapLookup(env))
			if err == nil {
				t.Fatal("load() error = nil, want required-value error")
			}
			if !strings.Contains(err.Error(), missing) {
				t.Fatalf("load() error = %q, want name %q", err, missing)
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("load() error leaked secret %q: %q", secret, err)
				}
			}
		})
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func mergeEnv(base, overrides map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(overrides))
	for name, value := range base {
		result[name] = value
	}
	for name, value := range overrides {
		result[name] = value
	}
	return result
}
