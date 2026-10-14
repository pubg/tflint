package tflint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bgentry/go-netrc/netrc"
	"github.com/hashicorp/terraform-svchost"
	"github.com/hashicorp/terraform-svchost/auth"
	"github.com/spf13/afero"
)

// Reuse Terraform's credential file formats without importing its internal CLI
// loader. Look up credentials per request so redirects cannot forward another
// host's token, and leave the caller's request unchanged.
type configCredentialsTransport struct {
	fs   afero.Afero
	base http.RoundTripper
}

func (t *configCredentialsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	request := req.Clone(req.Context())
	if err := PrepareRequestCredentials(t.fs, request, request.URL.Host); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}

// PrepareRequestCredentials applies Terraform JSON or netrc credentials for host
// to an HTTPS request. An existing Authorization header takes precedence.
// The host can differ from the request URL when a service uses a separate API host.
func PrepareRequestCredentials(fs afero.Afero, req *http.Request, host string) error {
	if req.URL.Scheme != "https" || req.Header.Get("Authorization") != "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(home, ".terraform.d")
	netrcName := ".netrc"
	if runtime.GOOS == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		configDir = filepath.Join(dir, "terraform.d")
		netrcName = "_netrc"
	}

	filename := filepath.Join(configDir, "credentials.tfrc.json")
	src, err := readConfigCredentials(fs, filename)
	if err != nil {
		return err
	}
	if src != nil {
		var credentials struct {
			Credentials map[string]struct {
				Token string `json:"token"`
			} `json:"credentials"`
		}
		if err := json.Unmarshal(src, &credentials); err != nil {
			// Parser errors can contain credential values; report only the file.
			return fmt.Errorf("invalid JSON credentials file %q", filename)
		}
		host, err := svchost.ForComparison(svchost.ForDisplay(host))
		if err == nil {
			for givenHost, credential := range credentials.Credentials {
				candidate, err := svchost.ForComparison(svchost.ForDisplay(givenHost))
				if err == nil && candidate == host && credential.Token != "" {
					auth.HostCredentialsToken(credential.Token).PrepareRequest(req)
					return nil
				}
			}
		}
	}

	// Match go-getter's NETRC override and platform-specific default filename.
	filename = os.Getenv("NETRC")
	if filename == "" {
		filename = filepath.Join(home, netrcName)
	}
	src, err = readConfigCredentials(fs, filename)
	if err != nil || src == nil {
		return err
	}
	credentials, err := netrc.Parse(bytes.NewReader(src))
	if err != nil {
		return fmt.Errorf("invalid netrc credentials file %q", filename)
	}
	if machine := credentials.FindMachine(host); machine != nil {
		req.SetBasicAuth(machine.Login, machine.Password)
	}
	return nil
}

func readConfigCredentials(fs afero.Afero, filename string) ([]byte, error) {
	src, err := fs.ReadFile(filename)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file %q: %w", filename, err)
	}
	return src, nil
}
