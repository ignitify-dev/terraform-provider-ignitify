// SPDX-License-Identifier: MPL-2.0

// Package provider is the Ignitify Terraform provider.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

const (
	providerTypeName = "ignitify"
	userAgentPrefix  = "terraform-provider-ignitify/"
)

var _ provider.Provider = (*ignitifyProvider)(nil)

type ignitifyProvider struct {
	version string
	// getenv reads the environment; tests replace it.
	getenv func(string) string
}

type providerModel struct {
	APIKey   types.String `tfsdk:"api_key"`
	Endpoint types.String `tfsdk:"endpoint"`
}

// New returns the provider factory for this build's version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &ignitifyProvider{version: version, getenv: os.Getenv}
	}
}

func (p *ignitifyProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = providerTypeName
	resp.Version = p.version
}

func (p *ignitifyProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage an existing Ignitify tenant: users, groups, API keys, secrets, tasks, deployments and hooks.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Description: "The API key. Defaults to the " + envAPIKey + " environment variable. " +
					"Users, groups and keys need an owner key; secrets, tasks, deployments and hooks work with a user key that holds their permissions.",
				Optional:  true,
				Sensitive: true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The API base URL. Defaults to the " + envAPIURL + " environment variable, else " + defaultEndpoint + ". " +
					"Must be https; plain http is accepted only to this machine.",
				Optional: true,
			},
		},
	}
}

func (p *ignitifyProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	settings, err := resolveSettings(configInput{
		apiKey:          cfg.APIKey,
		endpoint:        cfg.Endpoint,
		envAPIKey:       p.getenv(envAPIKey),
		envEndpoint:     p.getenv(envAPIURL),
		userAgentSuffix: p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Ignitify provider configuration", err.Error())
		return
	}
	c, err := client.New(client.Config{Endpoint: settings.endpoint, APIKey: settings.apiKey, UserAgent: userAgentPrefix + settings.version})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Ignitify provider configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *ignitifyProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newUserResource,
		newGroupResource,
		newAPIKeyResource,
		newSecretResource,
		newTaskResource,
		newDeploymentResource,
		newHookResource,
		newScheduleResource,
	}
}

func (p *ignitifyProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newWhoamiDataSource,
	}
}
