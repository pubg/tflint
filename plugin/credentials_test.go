package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/terraform-linters/tflint/tflint"
)

func pluginCredentialPaths(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	for _, name := range []string{
		"NETRC", "GITHUB_TOKEN", "GITHUB_TOKEN_github_com", "GITHUB_TOKEN_github_example_com",
		"GITLAB_TOKEN", "GITLAB_TOKEN_gitlab_example_com", "GITLAB_TOKEN_other_example_com",
		"CI_JOB_TOKEN", "CI_SERVER_URL",
	} {
		t.Setenv(name, "")
		// An empty GitHub host token deliberately masks the global token.
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	dir, netrcName := ".terraform.d", ".netrc"
	if runtime.GOOS == "windows" {
		dir, netrcName = "terraform.d", "_netrc"
	}
	if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, dir, "credentials.tfrc.json"), filepath.Join(home, netrcName)
}

func requestPluginMetadata(ctx context.Context, config *InstallConfig) error {
	if config.SourceType == "gitlab" {
		_, err := config.fetchGitLabReleaseAssets(ctx, newGitLabClient(config))
		return err
	}
	client, err := newGitHubClient(ctx, config)
	if err != nil {
		return err
	}
	_, _, err = client.Repositories.Get(ctx, "owner", "repo")
	return err
}

func TestPluginFileCredentials(t *testing.T) {
	for _, provider := range []struct {
		name, sourceType, host, apiHost, variable, envHeader, envPrefix string
	}{
		{"GitHub", "github", "github.com", "api.github.com", "GITHUB_TOKEN", "Authorization", "Bearer "},
		{"GitHub Enterprise", "github", "github.example.com", "github.example.com", "GITHUB_TOKEN", "Authorization", "Bearer "},
		{"GitLab", "gitlab", "gitlab.example.com", "gitlab.example.com", "GITLAB_TOKEN", "PRIVATE-TOKEN", ""},
	} {
		t.Run(provider.name, func(t *testing.T) {
			for _, test := range []struct {
				name, token, jsonToken, netrc, want string
				invalidJSON, wantErr                bool
				status                              int
			}{
				{name: "anonymous"},
				{name: "JSON token", jsonToken: "file-token", want: "Bearer file-token"},
				{name: "netrc", netrc: "login user password pass", want: "Basic dXNlcjpwYXNz"},
				{name: "JSON precedes netrc", jsonToken: "file-token", netrc: "login user password pass", want: "Bearer file-token"},
				{name: "empty JSON falls back to netrc", netrc: "login user password pass", want: "Basic dXNlcjpwYXNz"},
				{name: "legacy environment precedes files", token: "env-token", jsonToken: "file-token", netrc: "login user password pass"},
				{name: "legacy environment does not parse files", token: "env-token", invalidJSON: true},
				{name: "invalid JSON stops before netrc", invalidJSON: true, netrc: "login user password pass", wantErr: true},
				{name: "environment rejection does not fall back", token: "env-token", jsonToken: "file-token", status: http.StatusUnauthorized, wantErr: true},
				{name: "JSON rejection does not fall back", jsonToken: "file-token", netrc: "login user password pass", want: "Bearer file-token", status: http.StatusNotFound, wantErr: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					jsonPath, netrcPath := pluginCredentialPaths(t)
					credentials := fmt.Sprintf(`{"credentials":{%q:{"token":%q}}}`, provider.host, test.jsonToken)
					if test.invalidJSON {
						credentials = "invalid-sensitive-data"
					}
					if err := os.WriteFile(jsonPath, []byte(credentials), 0o600); err != nil {
						t.Fatal(err)
					}
					if test.netrc != "" {
						if err := os.WriteFile(netrcPath, []byte("machine "+provider.host+" "+test.netrc), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					t.Setenv(provider.variable, test.token)
					requests := 0
					original := http.DefaultTransport
					http.DefaultTransport = gitLabTestTransport(func(req *http.Request) (*http.Response, error) {
						requests++
						if req.URL.Host != provider.apiHost {
							t.Errorf("unexpected API host: %s", req.URL.Host)
						}
						for _, header := range []string{"Authorization", "PRIVATE-TOKEN", "JOB-TOKEN"} {
							want := ""
							if test.token != "" && header == provider.envHeader {
								want = provider.envPrefix + test.token
							} else if test.token == "" && header == "Authorization" {
								want = test.want
							}
							if req.Header.Get(header) != want {
								t.Errorf("unexpected %s header", header)
							}
						}
						status := test.status
						if status == 0 {
							status = http.StatusOK
						}
						return &http.Response{
							StatusCode: status, Header: make(http.Header), Request: req,
							Body: io.NopCloser(strings.NewReader(`{"assets":{"links":[]}}`)),
						}, nil
					})
					t.Cleanup(func() { http.DefaultTransport = original })
					config := NewInstallConfig(tflint.EmptyConfig(), &tflint.PluginConfig{SourceType: provider.sourceType, SourceHost: provider.host})
					err := requestPluginMetadata(t.Context(), config)
					if (err != nil) != test.wantErr {
						t.Fatalf("unexpected request error: %v", err)
					}
					wantRequests := 1
					if test.invalidJSON && test.token == "" {
						wantRequests = 0
						if err == nil || !strings.Contains(err.Error(), "invalid JSON credentials file") || strings.Contains(err.Error(), "sensitive-data") {
							t.Fatalf("expected redacted credentials error: %v", err)
						}
					}
					if requests != wantRequests {
						t.Errorf("got %d requests, want %d", requests, wantRequests)
					}
				})
			}
		})
	}
}

func TestPluginFileCredentialsRedirect(t *testing.T) {
	for _, sourceType := range []string{"github", "gitlab"} {
		t.Run(sourceType, func(t *testing.T) {
			for _, targetHost := range []string{"source.example.com", "cdn.source.example.com", "source.example.com:8443", "other.example.com"} {
				t.Run(targetHost, func(t *testing.T) {
					jsonPath, _ := pluginCredentialPaths(t)
					if err := os.WriteFile(jsonPath, []byte(`{"credentials":{"source.example.com":{"token":"file-token"}}}`), 0o600); err != nil {
						t.Fatal(err)
					}
					requests := 0
					original := http.DefaultTransport
					http.DefaultTransport = gitLabTestTransport(func(req *http.Request) (*http.Response, error) {
						requests++
						want := ""
						if req.URL.Host == "source.example.com" {
							want = "Bearer file-token"
						}
						if req.Header.Get("Authorization") != want {
							t.Errorf("unexpected Authorization for host %s", req.URL.Host)
						}
						if requests == 1 {
							return &http.Response{
								StatusCode: http.StatusFound, Request: req,
								Header: http.Header{"Location": {"https://" + targetHost + "/asset"}},
								Body:   io.NopCloser(strings.NewReader("")),
							}, nil
						}
						return &http.Response{
							StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
							Body: io.NopCloser(strings.NewReader(`{"assets":{"links":[]}}`)),
						}, nil
					})
					t.Cleanup(func() { http.DefaultTransport = original })
					config := NewInstallConfig(tflint.EmptyConfig(), &tflint.PluginConfig{SourceType: sourceType, SourceHost: "source.example.com"})
					if err := requestPluginMetadata(t.Context(), config); err != nil {
						t.Fatal(err)
					}
					if requests != 2 {
						t.Fatalf("got %d requests, want 2", requests)
					}
				})
			}
		})
	}
}
