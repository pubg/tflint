package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/terraform-linters/tflint/formatter"
)

func TestRemoteConfigInheritance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
  "plugin": {"terraform": {"enabled": true, "preset": "all"}},
  "rule": {
    "terraform_naming_convention": {"enabled": true, "format": "snake_case"},
    "terraform_required_version": {"enabled": true}
  }
}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	config := fmt.Sprintf(`
config { inherit_from = %q }
plugin "terraform" { preset = "recommended" }
rule "terraform_required_version" { enabled = false }
`, server.URL+"/base.json?ref=pinned")
	for name, contents := range map[string]string{
		".tflint.hcl": config,
		"main.tf": `terraform {
  required_providers {
    null = {
      source = "hashicorp/null"
      version = "~> 3.0"
    }
  }
}
resource "null_resource" "BadName" {}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name   string
		args   []string
		issues int
	}{
		{name: "init", args: []string{"--init"}},
		{name: "inspect", args: []string{"--format=json"}, issues: 1},
		{name: "CLI override", args: []string{"--format=json", "--disable-rule=terraform_naming_convention"}},
		{name: "recursive", args: []string{"--format=json", "--recursive"}, issues: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--chdir=" + dir}, test.args...)
			command := exec.Command("tflint", args...)
			output, err := command.Output()
			if test.issues > 0 {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
					t.Fatalf("expected lint issues, got %v, output=%s", err, output)
				}
			} else if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					t.Fatalf("command failed: %v, stderr=%s", err, exit.Stderr)
				}
				t.Fatal(err)
			}
			if test.name == "init" {
				if !strings.Contains(string(output), "All plugins are already installed") {
					t.Fatalf("unexpected initialization output: %s", output)
				}
				return
			}
			var result formatter.JSONOutput
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Issues) != test.issues {
				t.Fatalf("expected %d issues, got %s", test.issues, output)
			}
			if test.issues > 0 && result.Issues[0].Rule.Name != "terraform_naming_convention" {
				t.Fatalf("unexpected rule: %s", output)
			}
		})
	}
}
