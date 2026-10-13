package tflint

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/afero"
	"github.com/terraform-linters/tflint-plugin-sdk/hclext"
	"github.com/terraform-linters/tflint/terraform"
)

func inheritanceFS(t *testing.T, files map[string]string) afero.Afero {
	t.Helper()
	fs := afero.Afero{Fs: afero.NewMemMapFs()}
	for name, src := range files {
		if err := fs.WriteFile(filepath.FromSlash(name), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return fs
}

func TestLoadConfigInheritance(t *testing.T) {
	files := map[string]string{
		"base.hcl": `
tflint { required_version = ">= 0" }
config {
  force = true
  disabled_by_default = true
  call_module_type = "all"
  varfile = ["base.tfvars"]
  variables = ["foo=base"]
  ignore_module = { base = true }
}
plugin "terraform" { enabled = false }
plugin "example" {
  enabled = true
  source = "github.com/example/ruleset"
  version = "1.0.0"
  signature = "pgp"
  signing_key = "key"
  inherited = "base"
  custom = "base"
}
rule "example_rule" {
  enabled = true
  ignorable = false
  pattern = "base"
  nested {
    value = "base"
    removed = true
  }
}
rule "other_rule" { enabled = false }
`,
		"project/.tflint.hcl": `
config {
  inherit_from = "../base.hcl"
  force = false
  disabled_by_default = false
  varfile = []
  variables = ["foo=child"]
  ignore_module = { child = true }
}
plugin "example" {
  version = "2.0.0"
  custom = "child"
}
rule "example_rule" {
  pattern = "child"
  nested { value = "child" }
}
`,
	}
	cfg, err := LoadConfig(inheritanceFS(t, files), filepath.FromSlash("project/.tflint.hcl"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Force || cfg.DisabledByDefault || cfg.CallModuleType != terraform.CallAllModule {
		t.Fatalf("unexpected inherited options: force=%t disabled_by_default=%t call_module_type=%s", cfg.Force, cfg.DisabledByDefault, cfg.CallModuleType)
	}
	if len(cfg.Varfiles) != 0 || !cmp.Equal(cfg.Variables, []string{"foo=child"}) || !cmp.Equal(cfg.IgnoreModules, map[string]bool{"child": true}) {
		t.Fatalf("collections were not replaced: varfile=%v variables=%v ignore_module=%v", cfg.Varfiles, cfg.Variables, cfg.IgnoreModules)
	}
	plugin := cfg.Plugins["example"]
	if !plugin.Enabled || plugin.Version != "2.0.0" || plugin.Source != "github.com/example/ruleset" || plugin.SigningKey != "key" || plugin.Signature != "pgp" {
		t.Fatalf("plugin attributes were not inherited: %#v", plugin)
	}
	content, diags := plugin.Content(&hclext.BodySchema{Attributes: []hclext.AttributeSchema{
		{Name: "inherited", Required: true}, {Name: "custom", Required: true},
	}})
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	for name, want := range map[string]string{"inherited": "base", "custom": "child"} {
		value, diags := content.Attributes[name].Expr.Value(nil)
		if diags.HasErrors() || value.AsString() != want {
			t.Fatalf("unexpected %s: %s, %s", name, value.GoString(), diags)
		}
	}
	rule := cfg.Rules["example_rule"]
	if !rule.Enabled || rule.isIgnorable() || cfg.Plugins["terraform"].Enabled || cfg.Rules["other_rule"].Enabled {
		t.Fatal("inherited rule settings or disabled bundled plugin were lost")
	}
	ruleContent, diags := rule.Body.Content(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "pattern", Required: true}},
		Blocks:     []hcl.BlockHeaderSchema{{Type: "nested"}},
	})
	if diags.HasErrors() || len(ruleContent.Blocks) != 1 {
		t.Fatalf("unexpected rule content: %#v, %s", ruleContent, diags)
	}
	nested, diags := ruleContent.Blocks[0].Body.JustAttributes()
	if diags.HasErrors() || nested["removed"] != nil {
		t.Fatalf("nested block was not replaced: %#v, %s", nested, diags)
	}
	value, diags := ruleContent.Attributes["pattern"].Expr.Value(nil)
	if diags.HasErrors() || value.AsString() != "child" {
		t.Fatalf("unexpected pattern: %s, %s", value.GoString(), diags)
	}
	for name, want := range files {
		if got := string(cfg.Sources()[filepath.FromSlash(name)]); got != want {
			t.Fatalf("source %s was not retained", name)
		}
	}
	if cfg.IsJSONConfig() {
		t.Fatal("HCL inheritance should not require JSON support")
	}
	// Keep the established CLI override behavior after file inheritance.
	cfg.Merge(&Config{Rules: map[string]*RuleConfig{"example_rule": {Enabled: false}}})
	if cfg.Rules["example_rule"].Enabled {
		t.Fatal("CLI override did not take precedence")
	}
}

func TestLoadConfigInheritanceJSON(t *testing.T) {
	for _, test := range []struct {
		name, root, base string
		files            map[string]string
	}{
		{
			name: "JSON parent", root: "child.hcl", base: "base.json",
			files: map[string]string{
				"child.hcl": `config { inherit_from = "base.json" }`,
				"base.json": `{"config":{"force":true},"plugin":{"terraform":{"enabled":false}}}`,
			},
		},
		{
			name: "JSON child", root: "child.json", base: "base.hcl",
			files: map[string]string{
				"child.json": `{"config":{"inherit_from":"base.hcl"}}`,
				"base.hcl":   "config { force = true }\nplugin \"terraform\" { enabled = false }",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := LoadConfig(inheritanceFS(t, test.files), test.root)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Force || cfg.Plugins["terraform"].Enabled || !cfg.IsJSONConfig() {
				t.Fatal("mixed HCL/JSON inheritance was not applied")
			}
			if string(cfg.Sources()[test.base]) != test.files[test.base] {
				t.Fatal("parent source was not retained")
			}
		})
	}
}

func TestLoadConfigInheritanceErrors(t *testing.T) {
	for _, test := range []struct {
		name, child, parent, want string
	}{
		{name: "empty", child: `config { inherit_from = "" }`, want: "must not be empty"},
		{name: "list", child: `config { inherit_from = ["base.hcl"] }`, want: "string required"},
		{name: "missing", child: `config { inherit_from = "missing.hcl" }`, want: "failed to read inherited config"},
		{name: "cycle", child: `config { inherit_from = "./child.hcl" }`, want: "circular config inheritance"},
		{name: "indirect cycle", child: `config { inherit_from = "base.hcl" }`, parent: `config { inherit_from = "child.hcl" }`, want: "circular config inheritance"},
		{name: "scheme", child: `config { inherit_from = "ftp://example.com/base.hcl" }`, want: "unsupported inherit_from URL scheme"},
		{name: "invalid URL", child: `config { inherit_from = "https://example.com/%zz" }`, want: "invalid inherit_from"},
		{name: "invalid parent", child: `config { inherit_from = "base.hcl" }`, parent: `config {`, want: "Unclosed configuration block"},
		{name: "unknown attribute", child: `config { inherit_from = "base.hcl" }`, parent: `config { unknown = true }`, want: "Unsupported argument"},
		{name: "parent version constraint", child: "config { inherit_from = \"base.hcl\" }\ntflint { required_version = \">= 0\" }", parent: `tflint { required_version = ">= 999.0" }`, want: "Unsupported TFLint version"},
		{name: "missing enabled", child: "config { inherit_from = \"base.hcl\" }\nrule \"custom\" { value = true }", parent: "", want: "Missing required argument"},
		{name: "invalid effective plugin", child: `config { inherit_from = "base.hcl" }`, parent: "plugin \"example\" {\n enabled = true\n version = \"1.0\"\n}", want: `"source" attribute cannot be omitted`},
		{name: "duplicate inheritance", child: "config { inherit_from = \"base.hcl\" }\nconfig { inherit_from = \"other.hcl\" }", want: "only one inherit_from"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadConfig(inheritanceFS(t, map[string]string{"child.hcl": test.child, "base.hcl": test.parent}), "child.hcl")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestLoadConfigInheritanceDepth(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= maxConfigInheritanceDepth; i++ {
		files[fmt.Sprintf("%d.hcl", i)] = fmt.Sprintf("config { inherit_from = %q }", fmt.Sprintf("%d.hcl", i+1))
	}
	_, err := LoadConfig(inheritanceFS(t, files), "0.hcl")
	if err == nil || !strings.Contains(err.Error(), "config inheritance exceeds") {
		t.Fatalf("expected inheritance depth error, got %v", err)
	}
}

func TestLoadConfigRemoteInheritance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/v1/base.json?ref=pinned", http.StatusFound)
		case "/v1/base.json":
			fmt.Fprint(w, `{"config":{"inherit_from":"grandparent.hcl"},"plugin":{"example":{"version":"2.0.0"}}}`)
		case "/v1/grandparent.hcl":
			fmt.Fprint(w, "config { force = true }\nplugin \"example\" {\n enabled = true\n source = \"github.com/example/ruleset\"\n version = \"1.0.0\"\n custom = \"remote\"\n}")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	child := fmt.Sprintf("config { inherit_from = %q }\nplugin \"example\" { custom = \"local\" }", server.URL+"/start")
	cfg, err := LoadConfig(inheritanceFS(t, map[string]string{"child.hcl": child}), "child.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Force || !cfg.IsJSONConfig() || cfg.Plugins["example"].Version != "2.0.0" || !cfg.Plugins["example"].Enabled {
		t.Fatal("remote inheritance chain was not applied")
	}
	if cfg.Sources()[server.URL+"/v1/base.json?ref=pinned.json"] == nil || cfg.Sources()[server.URL+"/v1/grandparent.hcl"] == nil {
		t.Fatal("remote source filenames were not preserved for plugin decoding")
	}
	content, diags := cfg.Plugins["example"].Content(&hclext.BodySchema{Mode: hclext.SchemaJustAttributesMode})
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	value, diags := content.Attributes["custom"].Expr.Value(nil)
	if diags.HasErrors() || value.AsString() != "local" {
		t.Fatalf("local custom attribute did not override remote: %s, %s", value.GoString(), diags)
	}
}

func TestLoadConfigRemoteInheritanceErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large.hcl":
			fmt.Fprint(w, strings.Repeat(" ", maxRemoteConfigSize+1))
		case "/cycle.hcl":
			fmt.Fprint(w, `config { inherit_from = "cycle.hcl" }`)
		case "/local.hcl":
			fmt.Fprint(w, `config { inherit_from = "file:///local.hcl" }`)
		case "/invalid.hcl":
			fmt.Fprint(w, "config {")
		case "/redirect":
			http.Redirect(w, r, "/redirect", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for path, want := range map[string]string{
		"/missing.hcl": "404 Not Found",
		"/large.hcl":   "exceeds 1048576 bytes",
		"/cycle.hcl":   "circular config inheritance",
		"/local.hcl":   "unsupported inherit_from URL scheme",
		"/invalid.hcl": "Unclosed configuration block",
		"/redirect":    "stopped after 10 redirects",
	} {
		t.Run(path, func(t *testing.T) {
			child := fmt.Sprintf("config { inherit_from = %q }", server.URL+path)
			_, err := LoadConfig(inheritanceFS(t, map[string]string{"child.hcl": child}), "child.hcl")
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want error containing %q, got %v", want, err)
			}
		})
	}
}

func TestLoadConfigInheritanceLogs(t *testing.T) {
	var output bytes.Buffer
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	const parent = "# confidential-body-comment\nconfig { force = true }"
	const child = `config { inherit_from = "parent.hcl?token=nested-query-secret#fragment-secret" }`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/base.hcl?token=redirect-query-secret", http.StatusFound)
		case "/base.hcl":
			fmt.Fprint(w, child)
		case "/parent.hcl":
			fmt.Fprint(w, parent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	t.Run("nested inheritance and redacted URLs", func(t *testing.T) {
		output.Reset()
		withUserinfo := strings.Replace(server.URL, "://", "://username-secret:password-secret@", 1)
		files := map[string]string{
			"root.hcl":  `config { inherit_from = "local.hcl" }`,
			"local.hcl": fmt.Sprintf("config { inherit_from = %q }", withUserinfo+"/start?token=initial-query-secret#fragment-secret"),
		}
		cfg, err := LoadConfig(inheritanceFS(t, files), "root.hcl")
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Force {
			t.Fatal("nested inherited configuration was not applied")
		}
		logs := output.String()
		for _, want := range []string{
			"[DEBUG] Load inherited config file: local.hcl (depth=1)",
			fmt.Sprintf("[DEBUG] Download inherited config: %s/start (depth=2)", server.URL),
			fmt.Sprintf("[DEBUG] Inherited config response: %s/base.hcl (status=200)", server.URL),
			fmt.Sprintf("[DEBUG] Downloaded inherited config: %s/base.hcl (%d bytes)", server.URL, len(child)),
			fmt.Sprintf("[DEBUG] Download inherited config: %s/parent.hcl (depth=3)", server.URL),
			fmt.Sprintf("[DEBUG] Downloaded inherited config: %s/parent.hcl (%d bytes)", server.URL, len(parent)),
		} {
			if !strings.Contains(logs, want) {
				t.Errorf("missing log %q in:\n%s", want, logs)
			}
		}
		for _, secret := range []string{"username-secret", "password-secret", "initial-query-secret", "redirect-query-secret", "nested-query-secret", "fragment-secret", "confidential-body-comment"} {
			if strings.Contains(logs, secret) {
				t.Errorf("debug logs exposed %q", secret)
			}
		}
	})

	t.Run("failed response", func(t *testing.T) {
		output.Reset()
		files := map[string]string{
			"root.hcl": fmt.Sprintf("config { inherit_from = %q }", server.URL+"/missing.hcl?token=initial-query-secret"),
		}
		_, err := LoadConfig(inheritanceFS(t, files), "root.hcl")
		if err == nil {
			t.Fatal("expected a download failure")
		}
		logs := output.String()
		want := fmt.Sprintf("[DEBUG] Inherited config response: %s/missing.hcl (status=404)", server.URL)
		if !strings.Contains(logs, want) || strings.Contains(logs, "Downloaded inherited config:") || strings.Contains(logs, "initial-query-secret") {
			t.Fatalf("unexpected failed download logs:\n%s", logs)
		}
	})
}
