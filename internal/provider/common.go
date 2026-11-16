// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// clientFrom takes the API client the provider passed to a resource or data
// source. Nil data means the provider is not configured yet (validation), and
// is not an error.
func clientFrom(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T; this is a bug in the provider", data))
		return nil
	}
	return c
}

// ownerKeyHint is the detail a user key gets on an owner-only route.
const ownerKeyHint = "Users, groups and API keys can be managed only with an owner key. " +
	"The provider is configured with a user key. Use an owner key for the access-control configuration, " +
	"and a user key for app resources such as secrets."

// apiErrorDiagnostic turns an API error into one error diagnostic, with a
// clear hint for the refusals a user can act on. Pure.
func apiErrorDiagnostic(action string, err error) diag.Diagnostic {
	summary := "Ignitify API error: " + action
	switch {
	case client.IsOwnerRequired(err):
		return diag.NewErrorDiagnostic(summary+": owner key required", ownerKeyHint+"\n\n"+err.Error())
	case client.HasReason(err, client.ReasonPermissionDenied):
		return diag.NewErrorDiagnostic(summary+": permission denied",
			"The API key's user lacks a permission this action needs. Add it to one of the user's groups.\n\n"+err.Error())
	case client.HasReason(err, client.ReasonNameTaken):
		return diag.NewErrorDiagnostic(summary+": name already used",
			"An object with this name already exists. Import it into the state, or choose another name.\n\n"+err.Error())
	default:
		return diag.NewErrorDiagnostic(summary, err.Error())
	}
}

// stringOrNull is a known string, or null when empty. Pure.
func stringOrNull(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// sortedCopy returns the values sorted, leaving the input untouched. Pure.
func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
