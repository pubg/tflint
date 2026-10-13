package tflint

import (
	"fmt"

	version "github.com/hashicorp/go-version"
)

// Version uses build metadata to identify the fork while preserving version constraint matching.
var Version *version.Version = version.Must(version.NewVersion("0.64.0+inherit.2"))

// ReferenceLink returns the rule reference link
func ReferenceLink(name string) string {
	return fmt.Sprintf("https://github.com/terraform-linters/tflint/blob/v%s/docs/rules/%s.md", Version.Core(), name)
}
