// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestResolveSettings(t *testing.T) {
	cases := []struct {
		name         string
		in           configInput
		wantKey      string
		wantEndpoint string
		wantErr      string
	}{
		{
			name:         "config wins over the environment",
			in:           configInput{apiKey: types.StringValue("fake-a"), endpoint: types.StringValue("https://api.example.test/"), envAPIKey: "fake-b", envEndpoint: "https://other.example.test"},
			wantKey:      "fake-a",
			wantEndpoint: "https://api.example.test",
		},
		{
			name:         "environment, then the default endpoint",
			in:           configInput{apiKey: types.StringNull(), endpoint: types.StringNull(), envAPIKey: "fake-b"},
			wantKey:      "fake-b",
			wantEndpoint: defaultEndpoint,
		},
		{
			name:         "plain http to loopback is allowed",
			in:           configInput{apiKey: types.StringValue("fake-a"), endpoint: types.StringValue("http://127.0.0.1:8180")},
			wantKey:      "fake-a",
			wantEndpoint: "http://127.0.0.1:8180",
		},
		{
			name:    "plain http to another host is refused",
			in:      configInput{apiKey: types.StringValue("fake-a"), endpoint: types.StringValue("http://api.example.test")},
			wantErr: "not https",
		},
		{
			name:    "no key",
			in:      configInput{apiKey: types.StringNull(), endpoint: types.StringNull()},
			wantErr: envAPIKey,
		},
		{
			name:    "unknown key",
			in:      configInput{apiKey: types.StringUnknown(), endpoint: types.StringNull()},
			wantErr: "must be known",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSettings(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.apiKey != tc.wantKey || got.endpoint != tc.wantEndpoint || got.version != devVersion {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestInputRules(t *testing.T) {
	cases := []struct {
		rule  string
		value string
		ok    bool
	}{
		{"name", "ci-bot", true},
		{"name", "0ops", true},
		{"name", "-ops", false},
		{"name", "Ops", false},
		{"name", strings.Repeat("a", 64), false},
		{"label", "deploy.bot_1", true},
		{"label", "a", true},
		{"label", "bot-", false},
		{"label", "-bot", false},
		{"label", "has space", false},
		{"label", strings.Repeat("a", 65), false},
		{"secret", "DB_PASSWORD", true},
		{"secret", "db_password", false},
		{"secret", "_X", false},
		{"secret", "A" + strings.Repeat("B", 64), false},
	}
	patterns := map[string]interface{ MatchString(string) bool }{
		"name":   accessNamePattern,
		"label":  keyLabelPattern,
		"secret": secretNamePattern,
	}
	for _, tc := range cases {
		if got := patterns[tc.rule].MatchString(tc.value); got != tc.ok {
			t.Errorf("%s %q: got %v, want %v", tc.rule, tc.value, got, tc.ok)
		}
	}
}
