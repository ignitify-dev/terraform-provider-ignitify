// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

func readWhoami(t *testing.T, c *client.Client) (tfsdk.State, bool) {
	t.Helper()
	ctx := context.Background()
	ds := newWhoamiDataSource().(*whoamiDataSource)
	var cresp datasource.ConfigureResponse
	ds.Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, &cresp)
	var sresp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &sresp)
	typ := sresp.Schema.Type().TerraformType(ctx)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Logf("diagnostics: %v", resp.Diagnostics)
		return resp.State, false
	}
	return resp.State, true
}

func TestWhoamiOwnerAndUserKey(t *testing.T) {
	fake, c := newFakeAPI(t)
	maxTasks := int64(10)
	fake.whoami = client.Whoami{
		TenantID: "tnt_test", Name: "acme", Status: "active",
		Tier:   client.WhoamiTier{Name: "team", Version: 3},
		Limits: client.WhoamiLimits{MaxTasks: &maxTasks, MaxStorageMiB: 512},
		Usage:  client.WhoamiUsage{TaskCount: 2},
		Access: &client.WhoamiAccess{Key: "key_owner", Groups: []string{}, Permissions: []string{"tasks.read", "tasks.write"}},
	}
	state, ok := readWhoami(t, c)
	if !ok {
		t.Fatal("read failed")
	}
	var owner types.Bool
	mustOK(t, state.GetAttribute(context.Background(), path.Root("is_owner_key"), &owner))
	if !owner.ValueBool() || !stringAt(t, state, "user").IsNull() {
		t.Fatal("an owner key has no user")
	}
	var maxTasksGot, maxUnits types.Int64
	mustOK(t, state.GetAttribute(context.Background(), path.Root("limits").AtName("max_tasks"), &maxTasksGot))
	mustOK(t, state.GetAttribute(context.Background(), path.Root("limits").AtName("max_units"), &maxUnits))
	if maxTasksGot.ValueInt64() != 10 || !maxUnits.IsNull() {
		t.Fatal("limits: a nil limit must be null (unlimited)")
	}
	if stringAt(t, state, "tier_name").ValueString() != "team" {
		t.Fatal("tier not read")
	}

	user := "ci"
	fake.whoami.Access = &client.WhoamiAccess{Key: "key_user", User: &user, Groups: []string{"deployers"}, Permissions: []string{"secrets.write"}}
	state, ok = readWhoami(t, c)
	if !ok {
		t.Fatal("read failed")
	}
	mustOK(t, state.GetAttribute(context.Background(), path.Root("is_owner_key"), &owner))
	if owner.ValueBool() || stringAt(t, state, "user").ValueString() != "ci" {
		t.Fatal("a user key names its user")
	}
}
