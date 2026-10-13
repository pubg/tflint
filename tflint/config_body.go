package tflint

import (
	"fmt"
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2"
)

// inheritedConfigBody preserves source expressions while overriding attributes.
// Top-level config, plugin, rule and tflint blocks merge; nested blocks replace.
type inheritedConfigBody struct {
	parent      hcl.Body
	child       hcl.Body
	mergeBlocks bool
}

func (b *inheritedConfigBody) Content(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Diagnostics) {
	content, _, diags := b.content(schema, false)
	return content, diags
}

func (b *inheritedConfigBody) PartialContent(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Body, hcl.Diagnostics) {
	return b.content(schema, true)
}

func (b *inheritedConfigBody) JustAttributes() (hcl.Attributes, hcl.Diagnostics) {
	parent, diags := b.parent.JustAttributes()
	child, childDiags := b.child.JustAttributes()
	attrs := hcl.Attributes{}
	maps.Copy(attrs, parent)
	maps.Copy(attrs, child)
	return attrs, append(diags, childDiags...)
}

func (b *inheritedConfigBody) MissingItemRange() hcl.Range {
	return b.child.MissingItemRange()
}

func (b *inheritedConfigBody) content(schema *hcl.BodySchema, partial bool) (*hcl.BodyContent, hcl.Body, hcl.Diagnostics) {
	// Required attributes may come from either file; check them after merging.
	optional := &hcl.BodySchema{Blocks: schema.Blocks}
	for _, attr := range schema.Attributes {
		optional.Attributes = append(optional.Attributes, hcl.AttributeSchema{Name: attr.Name})
	}
	var parent, child *hcl.BodyContent
	var parentRest, childRest hcl.Body
	var diags, childDiags hcl.Diagnostics
	if partial {
		parent, parentRest, diags = b.parent.PartialContent(optional)
		child, childRest, childDiags = b.child.PartialContent(optional)
	} else {
		parent, diags = b.parent.Content(optional)
		child, childDiags = b.child.Content(optional)
	}
	diags = append(diags, childDiags...)
	content := &hcl.BodyContent{Attributes: hcl.Attributes{}, MissingItemRange: b.MissingItemRange()}
	maps.Copy(content.Attributes, parent.Attributes)
	maps.Copy(content.Attributes, child.Attributes)
	for _, attr := range schema.Attributes {
		if attr.Required && content.Attributes[attr.Name] == nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing required argument",
				Detail:   fmt.Sprintf("The argument %q is required, but was not set.", attr.Name),
				Subject:  b.MissingItemRange().Ptr(),
			})
		}
	}
	for _, block := range parent.Blocks {
		if !slices.ContainsFunc(child.Blocks, func(other *hcl.Block) bool { return sameConfigBlock(block, other) }) {
			content.Blocks = append(content.Blocks, block)
		}
	}
	for _, block := range child.Blocks {
		override := *block
		if b.mergeBlocks {
			for _, base := range parent.Blocks {
				if sameConfigBlock(base, block) {
					override.Body = &inheritedConfigBody{parent: base.Body, child: override.Body}
				}
			}
		}
		content.Blocks = append(content.Blocks, &override)
	}
	var rest hcl.Body
	if partial {
		rest = &inheritedConfigBody{parent: parentRest, child: childRest, mergeBlocks: b.mergeBlocks}
	}
	return content, rest, diags
}

func sameConfigBlock(left, right *hcl.Block) bool {
	return left.Type == right.Type && slices.Equal(left.Labels, right.Labels)
}
