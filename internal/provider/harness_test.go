// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

// harness drives one resource's CRUD and import methods directly, the way the
// framework calls them, with no Terraform binary.
type harness struct {
	t      *testing.T
	ctx    context.Context
	res    resource.Resource
	schema schema.Schema
	typ    tftypes.Object
}

func newHarness(t *testing.T, newRes func() resource.Resource, c *client.Client) *harness {
	t.Helper()
	ctx := context.Background()
	res := newRes()
	var sresp resource.SchemaResponse
	res.Schema(ctx, resource.SchemaRequest{}, &sresp)
	if sresp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sresp.Diagnostics)
	}
	var cresp resource.ConfigureResponse
	res.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{ProviderData: c}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", cresp.Diagnostics)
	}
	typ, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return &harness{t: t, ctx: ctx, res: res, schema: sresp.Schema, typ: typ}
}

// object builds a value of the schema's object type. A missing attribute is
// null, or unknown when fill is set and the attribute is computed.
func (h *harness) object(values map[string]tftypes.Value, unknownComputed bool) tftypes.Value {
	attrs := map[string]tftypes.Value{}
	for name, attrType := range h.typ.AttributeTypes {
		if v, ok := values[name]; ok {
			attrs[name] = v
			continue
		}
		if unknownComputed && h.schema.Attributes[name].IsComputed() {
			attrs[name] = tftypes.NewValue(attrType, tftypes.UnknownValue)
			continue
		}
		attrs[name] = tftypes.NewValue(attrType, nil)
	}
	return tftypes.NewValue(h.typ, attrs)
}

// planned is the configuration as the plan holds it: write-only values null,
// unset computed values unknown.
func (h *harness) planned(config map[string]tftypes.Value) tftypes.Value {
	values := map[string]tftypes.Value{}
	for name, v := range config {
		if h.schema.Attributes[name].IsWriteOnly() {
			continue
		}
		values[name] = v
	}
	return h.object(values, true)
}

func (h *harness) emptyState() tfsdk.State {
	return tfsdk.State{Schema: h.schema, Raw: tftypes.NewValue(h.typ, nil)}
}

// modifyPlan runs the resource's plan modifier, when it has one, as a plan does.
func (h *harness) modifyPlan(cfg tfsdk.Config, plan tfsdk.Plan, prior tfsdk.State) (tfsdk.Plan, diag.Diagnostics) {
	modifier, ok := h.res.(resource.ResourceWithModifyPlan)
	if !ok {
		return plan, nil
	}
	resp := resource.ModifyPlanResponse{Plan: plan}
	modifier.ModifyPlan(h.ctx, resource.ModifyPlanRequest{Config: cfg, Plan: plan, State: prior}, &resp)
	return resp.Plan, resp.Diagnostics
}

func (h *harness) create(config map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	cfg := tfsdk.Config{Schema: h.schema, Raw: h.object(config, false)}
	plan, diags := h.modifyPlan(cfg, tfsdk.Plan{Schema: h.schema, Raw: h.planned(config)}, h.emptyState())
	if diags.HasError() {
		return h.emptyState(), diags
	}
	req := resource.CreateRequest{Config: cfg, Plan: plan}
	resp := resource.CreateResponse{State: h.emptyState(), Diagnostics: diags}
	h.res.Create(h.ctx, req, &resp)
	return resp.State, resp.Diagnostics
}

func (h *harness) read(state tfsdk.State) (tfsdk.State, diag.Diagnostics) {
	resp := resource.ReadResponse{State: state}
	h.res.Read(h.ctx, resource.ReadRequest{State: state}, &resp)
	return resp.State, resp.Diagnostics
}

// planUpdate plans a new configuration over a prior state. Computed values
// the configuration does not set keep their prior value, as
// UseStateForUnknown does; then the resource's plan modifier runs.
func (h *harness) planUpdate(prior tfsdk.State, config map[string]tftypes.Value) (tfsdk.Config, tfsdk.Plan, diag.Diagnostics) {
	priorAttrs := map[string]tftypes.Value{}
	if err := prior.Raw.As(&priorAttrs); err != nil {
		h.t.Fatalf("prior state: %v", err)
	}
	values := map[string]tftypes.Value{}
	for name, v := range priorAttrs {
		if h.schema.Attributes[name].IsComputed() && !h.schema.Attributes[name].IsOptional() {
			values[name] = v
		}
	}
	for name, v := range config {
		if !h.schema.Attributes[name].IsWriteOnly() {
			values[name] = v
		}
	}
	cfg := tfsdk.Config{Schema: h.schema, Raw: h.object(config, false)}
	plan, diags := h.modifyPlan(cfg, tfsdk.Plan{Schema: h.schema, Raw: h.object(values, true)}, prior)
	return cfg, plan, diags
}

// update applies a new configuration to a prior state.
func (h *harness) update(prior tfsdk.State, config map[string]tftypes.Value) (tfsdk.State, diag.Diagnostics) {
	cfg, plan, diags := h.planUpdate(prior, config)
	if diags.HasError() {
		return prior, diags
	}
	req := resource.UpdateRequest{Config: cfg, Plan: plan, State: prior}
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: h.schema, Raw: plan.Raw}, Diagnostics: diags}
	h.res.Update(h.ctx, req, &resp)
	return resp.State, resp.Diagnostics
}

func (h *harness) delete(state tfsdk.State) diag.Diagnostics {
	resp := resource.DeleteResponse{State: state}
	h.res.Delete(h.ctx, resource.DeleteRequest{State: state}, &resp)
	return resp.Diagnostics
}

// importAndRead imports by id and reads, as `terraform import` does.
func (h *harness) importAndRead(id string) (tfsdk.State, diag.Diagnostics) {
	resp := resource.ImportStateResponse{State: h.emptyState()}
	h.res.(resource.ResourceWithImportState).ImportState(h.ctx, resource.ImportStateRequest{ID: id}, &resp)
	if resp.Diagnostics.HasError() {
		return resp.State, resp.Diagnostics
	}
	return h.read(resp.State)
}

// mustOK fails the test on any error diagnostic.
func mustOK(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
}

// mustFail fails the test unless an error diagnostic's summary or detail holds want.
func mustFail(t *testing.T, diags diag.Diagnostics, want string) {
	t.Helper()
	for _, d := range diags.Errors() {
		if strings.Contains(d.Summary()+"\n"+d.Detail(), want) {
			return
		}
	}
	t.Fatalf("want an error with %q, got %v", want, diags)
}

func stringAt(t *testing.T, state tfsdk.State, name string) types.String {
	t.Helper()
	var v types.String
	mustOK(t, state.GetAttribute(context.Background(), path.Root(name), &v))
	return v
}

func setAt(t *testing.T, state tfsdk.State, name string) []string {
	t.Helper()
	var v types.Set
	mustOK(t, state.GetAttribute(context.Background(), path.Root(name), &v))
	var out []string
	mustOK(t, v.ElementsAs(context.Background(), &out, false))
	return sortedCopy(out)
}

func isRemoved(state tfsdk.State) bool {
	return state.Raw.IsNull()
}

func tfString(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

func tfInt(n int64) tftypes.Value {
	return tftypes.NewValue(tftypes.Number, new(big.Float).SetInt64(n))
}

func tfStringSet(values ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, tfString(v))
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}
