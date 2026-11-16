// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// TestProviderSchemaIsValid runs the framework's own schema checks (for
// example: a write-only attribute may not be computed) and pins the protocol
// facts the design depends on.
func TestProviderSchemaIsValid(t *testing.T) {
	server, err := providerserver.NewProtocol6WithError(New("test")())()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("schema error: %s: %s", d.Summary, d.Detail)
		}
	}
	for _, name := range []string{"ignitify_user", "ignitify_group", "ignitify_api_key", "ignitify_secret"} {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("resource %s not registered", name)
		}
	}
	if _, ok := resp.DataSourceSchemas["ignitify_whoami"]; !ok {
		t.Error("data source ignitify_whoami not registered")
	}
	attrs := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range resp.ResourceSchemas["ignitify_secret"].Block.Attributes {
		attrs[a.Name] = a
	}
	if v := attrs["value_wo"]; v == nil || !v.WriteOnly || !v.Sensitive {
		t.Error("ignitify_secret.value_wo must be write-only and sensitive")
	}
	for _, a := range resp.ResourceSchemas["ignitify_api_key"].Block.Attributes {
		if a.Name == "raw_key" && (!a.Sensitive || !a.Computed) {
			t.Error("ignitify_api_key.raw_key must be sensitive and computed")
		}
	}
}
