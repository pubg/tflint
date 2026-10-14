package plugin

import (
	"net/http"
	"strings"

	"github.com/spf13/afero"
	"github.com/terraform-linters/tflint/tflint"
)

// File credentials belong to the source host, even when its API uses another host.
type pluginCredentialsTransport struct {
	http.RoundTripper
	sourceHost string
	apiHost    string
}

func (t *pluginCredentialsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.EqualFold(req.URL.Host, t.apiHost) {
		req = req.Clone(req.Context())
		if err := tflint.PrepareRequestCredentials(afero.Afero{Fs: afero.NewOsFs()}, req, t.sourceHost); err != nil {
			return nil, err
		}
	}
	return t.RoundTripper.RoundTrip(req)
}
