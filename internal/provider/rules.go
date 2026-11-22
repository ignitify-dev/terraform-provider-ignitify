// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// The input rules the API applies, checked at plan time so a bad value fails
// before any call, and so the server never stores a value that differs from
// the configuration (which would show as drift).

// accessNamePattern is the user and group name rule.
var accessNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

const accessNameRule = "1 to 63 lower case letters, digits and '-', starting with a letter or digit"

// keyLabelPattern is a key label the server stores unchanged: 1 to 64 of
// `[A-Za-z0-9._-]`, not starting or ending with '-'. The server rewrites any
// other label, which would show as drift.
var keyLabelPattern = regexp.MustCompile(`^[A-Za-z0-9._](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9._])?$`)

const keyLabelRule = "1 to 64 letters, digits, '.', '_' and '-', not starting or ending with '-'"

// secretNamePattern is the secret name rule: an upper-case environment variable name.
var secretNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

const secretNameRule = "1 to 64 upper case letters, digits and '_', starting with a letter"

// taskSlugPattern is the task slug rule: 1 to 63 lower case letters, digits
// and '-', starting and ending with a letter or digit.
var taskSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

const taskSlugRule = "1 to 63 lower case letters, digits and '-', starting and ending with a letter or digit"

// permissionCatalogue is the fixed list of permissions a group may grant.
var permissionCatalogue = []string{
	"tasks.read",
	"tasks.write",
	"deployments.read",
	"deployments.write",
	"runs.read",
	"runs.write",
	"secrets.read",
	"secrets.write",
	"hooks.read",
	"hooks.write",
	"usage.read",
}

func accessNameValidator() validator.String {
	return stringvalidator.RegexMatches(accessNamePattern, "must be "+accessNameRule)
}

func keyLabelValidator() validator.String {
	return stringvalidator.RegexMatches(keyLabelPattern, "must be "+keyLabelRule)
}

func secretNameValidator() validator.String {
	return stringvalidator.RegexMatches(secretNamePattern, "must be "+secretNameRule)
}

func permissionValidator() validator.String {
	return stringvalidator.OneOf(permissionCatalogue...)
}

func taskSlugValidator() validator.String {
	return stringvalidator.RegexMatches(taskSlugPattern, "must be "+taskSlugRule)
}

// jsonTextValidator accepts a string that is one JSON value, as `jsonencode`
// makes it.
type jsonTextValidator struct{}

func jsonTextRule() validator.String { return jsonTextValidator{} }

func (jsonTextValidator) Description(context.Context) string { return "must be valid JSON" }

func (v jsonTextValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v jsonTextValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !json.Valid([]byte(req.ConfigValue.ValueString())) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid JSON", "The value "+v.Description(ctx)+"; write it with jsonencode(...).")
	}
}
