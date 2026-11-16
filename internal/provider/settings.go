// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/client"
)

const (
	envAPIKey       = "IGNITIFY_API_KEY"
	envAPIURL       = "IGNITIFY_API_URL"
	defaultEndpoint = "https://api.ignitify.dev"
	devVersion      = "dev"
)

// configInput is what the provider configuration is resolved from.
type configInput struct {
	apiKey          types.String
	endpoint        types.String
	envAPIKey       string
	envEndpoint     string
	userAgentSuffix string
}

// settings is the resolved provider configuration.
type settings struct {
	apiKey   string
	endpoint string
	version  string
}

// resolveSettings picks each value from the configuration, else the
// environment, else the default. The provider never reads a CLI credentials
// file. Pure.
func resolveSettings(in configInput) (settings, error) {
	if in.apiKey.IsUnknown() || in.endpoint.IsUnknown() {
		return settings{}, errors.New("api_key and endpoint must be known when the provider is configured; do not set them from values computed during apply")
	}
	apiKey := firstNonEmpty(in.apiKey.ValueString(), in.envAPIKey)
	if apiKey == "" {
		return settings{}, fmt.Errorf("no API key: set api_key in the provider block or the %s environment variable", envAPIKey)
	}
	endpoint, err := client.CheckEndpoint(firstNonEmpty(in.endpoint.ValueString(), in.envEndpoint, defaultEndpoint))
	if err != nil {
		return settings{}, err
	}
	return settings{apiKey: apiKey, endpoint: endpoint, version: firstNonEmpty(in.userAgentSuffix, devVersion)}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
