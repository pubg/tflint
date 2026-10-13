package tflint

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/spf13/afero"
)

const (
	maxConfigInheritanceDepth = 32
	maxRemoteConfigSize       = 1 << 20
)

type configLoader struct {
	fs       afero.Afero
	client   *http.Client
	loading  map[string]bool
	sources  map[string][]byte
	usesJSON bool
}

func loadConfigBody(fs afero.Afero, file afero.File) (hcl.Body, map[string][]byte, bool, error) {
	loader := &configLoader{
		fs: fs,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &configCredentialsTransport{fs: fs, base: http.DefaultTransport},
		},
		loading: map[string]bool{},
		sources: map[string][]byte{},
	}
	src, err := afero.ReadAll(file)
	if err != nil {
		return nil, nil, false, err
	}
	body, err := loader.load(file.Name(), nil, src)
	return body, loader.sources, loader.usesJSON, err
}

func (l *configLoader) load(name string, remote *url.URL, src []byte) (hcl.Body, error) {
	key := name
	if remote == nil {
		var err error
		key, err = filepath.Abs(name)
		if err != nil {
			return nil, err
		}
	}
	if l.loading[key] {
		return nil, fmt.Errorf("circular config inheritance: %s", name)
	}
	if len(l.loading) >= maxConfigInheritanceDepth {
		return nil, fmt.Errorf("config inheritance exceeds %d files", maxConfigInheritanceDepth)
	}
	l.loading[key] = true
	defer delete(l.loading, key)

	filename := name
	extension := filepath.Ext(name)
	if remote != nil {
		extension = filepath.Ext(remote.Path)
		if extension != ".json" {
			extension = ".hcl"
		}
		// Plugins reparse expressions using their source filename's extension.
		if !strings.HasSuffix(filename, extension) {
			filename += extension
		}
	}
	parser := hclparse.NewParser()
	var file *hcl.File
	var diags hcl.Diagnostics
	if extension == ".json" {
		file, diags = parser.ParseJSON(src, filename)
		l.usesJSON = true
	} else {
		file, diags = parser.ParseHCL(src, filename)
	}
	if diags.HasErrors() {
		return nil, diags
	}
	for source, contents := range parser.Sources() {
		l.sources[source] = contents
	}
	// Every file must accept this TFLint version, including overridden parents.
	if diags := checkVersionRequirement(file.Body); diags.HasErrors() {
		return nil, diags
	}

	content, _, diags := file.Body.PartialContent(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{{Type: "config"}},
	})
	if diags.HasErrors() {
		return nil, diags
	}
	var inherited *hcl.Attribute
	for _, block := range content.Blocks {
		content, _, diags := block.Body.PartialContent(&hcl.BodySchema{
			Attributes: []hcl.AttributeSchema{{Name: "inherit_from"}},
		})
		if diags.HasErrors() {
			return nil, diags
		}
		if attr := content.Attributes["inherit_from"]; attr != nil {
			if inherited != nil {
				return nil, fmt.Errorf("only one inherit_from is allowed in %s", name)
			}
			inherited = attr
		}
	}
	if inherited == nil {
		return file.Body, nil
	}
	var reference string
	if diags := gohcl.DecodeExpression(inherited.Expr, nil, &reference); diags.HasErrors() {
		return nil, diags
	}
	if strings.TrimSpace(reference) == "" {
		return nil, fmt.Errorf("inherit_from must not be empty in %s", name)
	}
	parent, err := l.loadParent(name, remote, reference)
	if err != nil {
		return nil, fmt.Errorf("failed to inherit config in %s: %w", name, err)
	}
	return &inheritedConfigBody{parent: parent, child: file.Body, mergeBlocks: true}, nil
}

func (l *configLoader) loadParent(name string, remote *url.URL, reference string) (hcl.Body, error) {
	if len(l.loading) >= maxConfigInheritanceDepth {
		return nil, fmt.Errorf("config inheritance exceeds %d files", maxConfigInheritanceDepth)
	}
	if remote == nil && filepath.IsAbs(reference) {
		return l.loadLocal(reference)
	}
	ref, err := url.Parse(reference)
	if err != nil {
		return nil, fmt.Errorf("invalid inherit_from: %w", err)
	}
	if remote != nil {
		ref = remote.ResolveReference(ref)
	}
	if ref.Scheme == "" {
		return l.loadLocal(filepath.Join(filepath.Dir(name), reference))
	}
	if ref.Scheme != "http" && ref.Scheme != "https" {
		return nil, fmt.Errorf("unsupported inherit_from URL scheme %q", ref.Scheme)
	}
	ref.Fragment = ""
	if l.loading[ref.String()] {
		return nil, fmt.Errorf("circular config inheritance: %s", ref)
	}
	log.Printf("[DEBUG] Download inherited config: %s (depth=%d)", configURLForLog(ref), len(l.loading))
	response, err := l.client.Get(ref.String())
	if err != nil {
		return nil, fmt.Errorf("failed to download inherited config: %w", err)
	}
	defer response.Body.Close()
	log.Printf("[DEBUG] Inherited config response: %s (status=%d)", configURLForLog(response.Request.URL), response.StatusCode)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download inherited config %s: %s", ref, response.Status)
	}
	src, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read inherited config %s: %w", ref, err)
	}
	if len(src) > maxRemoteConfigSize {
		return nil, fmt.Errorf("inherited config %s exceeds %d bytes", ref, maxRemoteConfigSize)
	}
	log.Printf("[DEBUG] Downloaded inherited config: %s (%d bytes)", configURLForLog(response.Request.URL), len(src))
	// Relative references in a redirected config use its final URL.
	return l.load(response.Request.URL.String(), response.Request.URL, src)
}

func (l *configLoader) loadLocal(name string) (hcl.Body, error) {
	log.Printf("[DEBUG] Load inherited config file: %s (depth=%d)", name, len(l.loading))
	src, err := l.fs.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("failed to read inherited config: %w", err)
	}
	return l.load(name, nil, src)
}

func configURLForLog(ref *url.URL) string {
	// URL userinfo and query parameters may contain credentials.
	safe := *ref
	safe.User = nil
	safe.RawQuery = ""
	safe.ForceQuery = false
	safe.Fragment = ""
	safe.RawFragment = ""
	return safe.String()
}
