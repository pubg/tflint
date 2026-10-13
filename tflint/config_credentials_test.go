package tflint

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func configCredentialTestPaths(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	t.Setenv("NETRC", "")
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "terraform.d", "credentials.tfrc.json"), filepath.Join(home, "_netrc")
	}
	return filepath.Join(home, ".terraform.d", "credentials.tfrc.json"), filepath.Join(home, ".netrc")
}

type configRoundTripFunc func(*http.Request) (*http.Response, error)

func (f configRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestConfigCredentials(t *testing.T) {
	const jsonToken = `{"credentials":{"example.com":{"token":"json-token"}}}`
	const basicCredentials = "machine example.com login user password pass"
	for _, test := range []struct {
		name, target, json, netrc, header, want, wantErr string
		overrideNetrc                                    bool
	}{
		{name: "anonymous"},
		{name: "JSON bearer", json: jsonToken, want: "Bearer json-token"},
		{name: "netrc basic", netrc: basicCredentials, want: "Basic dXNlcjpwYXNz"},
		{name: "JSON takes precedence", json: jsonToken, netrc: basicCredentials, want: "Bearer json-token"},
		{name: "normalized JSON hostname", json: `{"credentials":{"EXAMPLE.COM":{"token":"json-token"}}}`, want: "Bearer json-token"},
		{name: "different hostname", target: "https://other.example.com/base.hcl", json: jsonToken, netrc: basicCredentials},
		{name: "different port", target: "https://example.com:8443/base.hcl", json: jsonToken, netrc: basicCredentials},
		{name: "explicit authorization", json: "invalid-sensitive-data", header: "Bearer explicit", want: "Bearer explicit"},
		{name: "HTTP stays anonymous", target: "http://example.com/base.hcl", json: jsonToken, netrc: basicCredentials},
		{name: "HTTP does not read credentials", target: "http://example.com/base.hcl", json: "invalid-sensitive-data"},
		{name: "empty token falls back", json: `{"credentials":{"example.com":{"token":""}}}`, netrc: basicCredentials, want: "Basic dXNlcjpwYXNz"},
		{name: "invalid JSON", json: "invalid-sensitive-data", wantErr: "invalid JSON credentials file"},
		{name: "invalid token type", json: `{"credentials":{"example.com":{"token":["sensitive-data"]}}}`, wantErr: "invalid JSON credentials file"},
		{name: "invalid netrc", netrc: "default login sensitive-data\nmachine example.com login user password pass", wantErr: "invalid netrc credentials file"},
		{name: "unused netrc is not parsed", json: jsonToken, netrc: "default login sensitive-data\nmachine example.com login user password pass", want: "Bearer json-token"},
		{name: "NETRC override", netrc: basicCredentials, overrideNetrc: true, want: "Basic dXNlcjpwYXNz"},
		{name: "netrc default", netrc: "default login user password pass", want: "Basic dXNlcjpwYXNz"},
	} {
		t.Run(test.name, func(t *testing.T) {
			jsonPath, netrcPath := configCredentialTestPaths(t)
			files := map[string]string{}
			if test.json != "" {
				files[jsonPath] = test.json
			}
			if test.overrideNetrc {
				files[netrcPath] = "machine example.com login wrong password credentials"
				netrcPath = filepath.Join(filepath.Dir(netrcPath), "custom.netrc")
				t.Setenv("NETRC", netrcPath)
			}
			if test.netrc != "" {
				files[netrcPath] = test.netrc
			}
			target := test.target
			if target == "" {
				target = "https://example.com/base.hcl"
			}
			req, err := http.NewRequest(http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.header != "" {
				req.Header.Set("Authorization", test.header)
			}
			called := false
			transport := &configCredentialsTransport{
				fs: inheritanceFS(t, files),
				base: configRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					called = true
					if got := request.Header.Get("Authorization"); got != test.want {
						t.Errorf("unexpected Authorization: got %q, want %q", got, test.want)
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
				}),
			}
			response, err := transport.RoundTrip(req)
			if response != nil {
				response.Body.Close()
			}
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) || strings.Contains(err.Error(), "sensitive-data") {
					t.Fatalf("expected safe credential error containing %q, got %v", test.wantErr, err)
				}
				if called {
					t.Error("request was sent despite invalid credentials")
				}
			} else if err != nil || !called {
				t.Fatalf("request failed: %v", err)
			}
			if req.Header.Get("Authorization") != test.header {
				t.Error("the caller's request was modified")
			}
		})
	}
}

func TestLoadConfigCredentialsRedirect(t *testing.T) {
	jsonPath, netrcPath := configCredentialTestPaths(t)
	gotAuth := make(chan string, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		fmt.Fprint(w, "config { force = true }")
	})
	destination := httptest.NewTLSServer(handler)
	defer destination.Close()
	plain := httptest.NewServer(handler)
	defer plain.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer source-token" {
			t.Error("source did not receive its own credentials")
		}
		target := destination.URL + "/base.hcl"
		if r.URL.Path == "/plain" {
			target = plain.URL + "/base.hcl"
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer source.Close()

	// Exercise the real loader using only the test servers' trusted certificates.
	transport := source.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs.AddCert(destination.Certificate())
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
		transport.CloseIdleConnections()
	})
	sourceURL, _ := url.Parse(source.URL)
	destinationURL, _ := url.Parse(destination.URL)
	plainURL, _ := url.Parse(plain.URL)
	for _, test := range []struct {
		name, target, extraJSON, netrc, want string
	}{
		{name: "different host has no credentials", target: source.URL},
		{name: "different host has its own token", target: source.URL, extraJSON: fmt.Sprintf(`,%q:{"token":"destination-token"}`, destinationURL.Host), want: "Bearer destination-token"},
		{name: "different host uses netrc", target: source.URL, netrc: fmt.Sprintf("machine %s login user password pass", destinationURL.Host), want: "Basic dXNlcjpwYXNz"},
		{name: "HTTP redirect stays anonymous", target: source.URL + "/plain", extraJSON: fmt.Sprintf(`,%q:{"token":"plain-token"}`, plainURL.Host)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fs := inheritanceFS(t, map[string]string{
				jsonPath:    fmt.Sprintf(`{"credentials":{%q:{"token":"source-token"}%s}}`, sourceURL.Host, test.extraJSON),
				netrcPath:   test.netrc,
				"child.hcl": fmt.Sprintf("config { inherit_from = %q }", test.target),
			})
			config, err := LoadConfig(fs, "child.hcl")
			if err != nil {
				t.Fatal(err)
			}
			if !config.Force {
				t.Fatal("redirected configuration was not applied")
			}
			if got := <-gotAuth; got != test.want {
				t.Errorf("redirect received %q, want %q", got, test.want)
			}
			for name := range config.Sources() {
				if strings.Contains(name, "token") {
					t.Errorf("source filename contains a credential: %s", name)
				}
			}
		})
	}
}

func TestConfigCredentialsDoNotRetryAuthentication(t *testing.T) {
	jsonPath, netrcPath := configCredentialTestPaths(t)
	requests := 0
	transport := &configCredentialsTransport{
		fs: inheritanceFS(t, map[string]string{
			jsonPath:  `{"credentials":{"example.com":{"token":"json-token"}}}`,
			netrcPath: "machine example.com login user password pass",
		}),
		base: configRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			if req.Header.Get("Authorization") != "Bearer json-token" {
				t.Error("wrong credentials selected")
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
		}),
	}
	client := &http.Client{Transport: transport}
	response, err := client.Get("https://example.com/base.hcl")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || requests != 1 {
		t.Fatal("authentication failure was retried")
	}
}

func TestLocalConfigDoesNotReadCredentials(t *testing.T) {
	jsonPath, netrcPath := configCredentialTestPaths(t)
	config, err := LoadConfig(inheritanceFS(t, map[string]string{
		jsonPath:    "invalid JSON",
		netrcPath:   "invalid netrc",
		"local.hcl": "config { force = true }",
	}), "local.hcl")
	if err != nil || !config.Force {
		t.Fatalf("local configuration depended on credentials: %v", err)
	}
}
