package bedrock

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestProviderWithBaseURL builds a provider through the real constructor so
// BaseURL normalization and env capture behave exactly as in production.
func newTestProviderWithBaseURL(t *testing.T, baseURL string) *BedrockProvider {
	t.Helper()
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
		},
	}
	config.CheckAndSetDefaults()
	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)
	return provider
}

// keyWithEndpoints returns a Key whose BedrockKeyConfig carries the given endpoint overrides.
func keyWithEndpoints(ep *schemas.BedrockEndpointsConfig) schemas.Key {
	return schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: ep,
		},
	}
}

// clearEndpointEnv blanks every endpoint-related env var so ambient AWS
// configuration on the host can't leak into env-capture tests.
func clearEndpointEnv(t *testing.T) {
	t.Helper()
	for _, name := range endpointEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "")
}

func TestEndpointBaseResolution(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		key     schemas.Key
		service bedrockService
		region  string
		want    string
	}{
		{"default runtime", "", schemas.Key{}, serviceBedrockRuntime, "us-east-1",
			"https://bedrock-runtime.us-east-1.amazonaws.com"},
		{"default control plane", "", schemas.Key{}, serviceBedrockControlPlane, "us-east-1",
			"https://bedrock.us-east-1.amazonaws.com"},
		{"default agent-runtime", "", schemas.Key{}, serviceBedrockAgentRuntime, "us-east-1",
			"https://bedrock-agent-runtime.us-east-1.amazonaws.com"},
		{"default mantle", "", schemas.Key{}, serviceBedrockMantle, "us-east-1",
			"https://bedrock-mantle.us-east-1.api.aws"},
		{"BaseURL overrides runtime", "https://vpce-1.example", schemas.Key{}, serviceBedrockRuntime, "us-east-1",
			"https://vpce-1.example"},
		{"BaseURL does not leak to control plane", "https://vpce-1.example", schemas.Key{}, serviceBedrockControlPlane, "us-east-1",
			"https://bedrock.us-east-1.amazonaws.com"},
		{"BaseURL does not leak to agent-runtime", "https://vpce-1.example", schemas.Key{}, serviceBedrockAgentRuntime, "us-east-1",
			"https://bedrock-agent-runtime.us-east-1.amazonaws.com"},
		{"BaseURL does not leak to mantle", "https://vpce-1.example", schemas.Key{}, serviceBedrockMantle, "us-east-1",
			"https://bedrock-mantle.us-east-1.api.aws"},
		{"key endpoint trailing slash trimmed", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{Runtime: "https://y/"}), serviceBedrockRuntime, "us-east-1",
			"https://y"},
		{"key endpoint beats BaseURL", "https://vpce-1.example",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{Runtime: "https://key-endpoint.example"}), serviceBedrockRuntime, "us-east-1",
			"https://key-endpoint.example"},
		{"DNSSuffix rewrites runtime", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{DNSSuffix: "c2s.ic.gov"}), serviceBedrockRuntime, "us-iso-east-1",
			"https://bedrock-runtime.us-iso-east-1.c2s.ic.gov"},
		{"DNSSuffix rewrites control plane", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{DNSSuffix: "c2s.ic.gov"}), serviceBedrockControlPlane, "us-iso-east-1",
			"https://bedrock.us-iso-east-1.c2s.ic.gov"},
		{"DNSSuffix rewrites agent-runtime", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{DNSSuffix: "c2s.ic.gov"}), serviceBedrockAgentRuntime, "us-iso-east-1",
			"https://bedrock-agent-runtime.us-iso-east-1.c2s.ic.gov"},
		{"DNSSuffix does not touch mantle", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{DNSSuffix: "c2s.ic.gov"}), serviceBedrockMantle, "us-iso-east-1",
			"https://bedrock-mantle.us-iso-east-1.api.aws"},
		{"explicit endpoint beats DNSSuffix", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{ControlPlane: "https://cp.example", DNSSuffix: "c2s.ic.gov"}), serviceBedrockControlPlane, "us-iso-east-1",
			"https://cp.example"},
		{"DNSSuffix stray dots trimmed", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{DNSSuffix: ".c2s.ic.gov."}), serviceBedrockRuntime, "us-iso-east-1",
			"https://bedrock-runtime.us-iso-east-1.c2s.ic.gov"},
		{"whitespace-only key endpoint behaves as unset", "",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{Runtime: "  "}), serviceBedrockRuntime, "us-east-1",
			"https://bedrock-runtime.us-east-1.amazonaws.com"},
		{"BaseURL with path prefix preserved", "https://gw.corp/bedrock", schemas.Key{}, serviceBedrockRuntime, "us-east-1",
			"https://gw.corp/bedrock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &BedrockProvider{networkConfig: schemas.NetworkConfig{BaseURL: tc.baseURL}}
			assert.Equal(t, tc.want, provider.endpointBase(tc.key, tc.service, tc.region))
		})
	}
}

// TestEndpointDefaultParity pins the zero-config resolver output to the exact
// hostname templates the provider hardcoded before the resolver existed.
func TestEndpointDefaultParity(t *testing.T) {
	provider := &BedrockProvider{}
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
	assert.Equal(t, "https://bedrock.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockControlPlane, "us-east-1"))
	assert.Equal(t, "https://bedrock-agent-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockAgentRuntime, "us-east-1"))
	assert.Equal(t, "https://bedrock-mantle.us-east-1.api.aws", provider.endpointBase(schemas.Key{}, serviceBedrockMantle, "us-east-1"))
	assert.Equal(t, "https://b.s3.us-east-1.amazonaws.com", provider.s3BucketBase(schemas.Key{}, "us-east-1", "b"))
}

func TestS3BucketBase(t *testing.T) {
	cases := []struct {
		name   string
		key    schemas.Key
		region string
		bucket string
		want   string
	}{
		{"virtual-hosted default", schemas.Key{}, "us-east-1", "b",
			"https://b.s3.us-east-1.amazonaws.com"},
		{"path-style with explicit key override",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{S3: "https://vpce-s3.example"}), "us-east-1", "b",
			"https://vpce-s3.example/b"},
		{"path-style trims trailing slash",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{S3: "https://vpce-s3.example/"}), "us-east-1", "b",
			"https://vpce-s3.example/b"},
		{"DNSSuffix stays virtual-hosted",
			keyWithEndpoints(&schemas.BedrockEndpointsConfig{DNSSuffix: "c2s.ic.gov"}), "us-iso-east-1", "b",
			"https://b.s3.us-iso-east-1.c2s.ic.gov"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &BedrockProvider{}
			assert.Equal(t, tc.want, provider.s3BucketBase(tc.key, tc.region, tc.bucket))
		})
	}
}

func TestEndpointEnvOverrides(t *testing.T) {
	t.Run("service-specific var applies to its service only", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://env-runtime.example")
		provider := newTestProviderWithBaseURL(t, "")
		assert.Equal(t, "https://env-runtime.example", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
		assert.Equal(t, "https://bedrock.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockControlPlane, "us-east-1"))
	})

	t.Run("generic var applies to all services", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL", "https://env-global.example")
		provider := newTestProviderWithBaseURL(t, "")
		assert.Equal(t, "https://env-global.example", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
		assert.Equal(t, "https://env-global.example", provider.endpointBase(schemas.Key{}, serviceBedrockControlPlane, "us-east-1"))
		assert.Equal(t, "https://env-global.example", provider.endpointBase(schemas.Key{}, serviceBedrockAgentRuntime, "us-east-1"))
		assert.Equal(t, "https://env-global.example", provider.endpointBase(schemas.Key{}, serviceBedrockMantle, "us-east-1"))
		assert.Equal(t, "https://env-global.example/b", provider.s3BucketBase(schemas.Key{}, "us-east-1", "b"))
	})

	t.Run("service-specific var wins over generic var", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL", "https://env-global.example")
		t.Setenv("AWS_ENDPOINT_URL_BEDROCK", "https://env-cp.example")
		provider := newTestProviderWithBaseURL(t, "")
		assert.Equal(t, "https://env-cp.example", provider.endpointBase(schemas.Key{}, serviceBedrockControlPlane, "us-east-1"))
		assert.Equal(t, "https://env-global.example", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
	})

	t.Run("explicit config beats env", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL", "https://env-global.example")
		provider := newTestProviderWithBaseURL(t, "https://base.example")
		key := keyWithEndpoints(&schemas.BedrockEndpointsConfig{ControlPlane: "https://key-cp.example"})
		assert.Equal(t, "https://base.example", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
		assert.Equal(t, "https://key-cp.example", provider.endpointBase(key, serviceBedrockControlPlane, "us-east-1"))
	})

	t.Run("mantle service-specific var applies to mantle only", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL_BEDROCK_MANTLE", "https://env-mantle.example")
		provider := newTestProviderWithBaseURL(t, "")
		assert.Equal(t, "https://env-mantle.example", provider.endpointBase(schemas.Key{}, serviceBedrockMantle, "us-east-1"))
		assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
	})

	t.Run("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS disables env capture", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL", "https://env-global.example")
		t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "https://env-runtime.example")
		t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "true")
		provider := newTestProviderWithBaseURL(t, "")
		assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
		assert.Equal(t, "https://bedrock.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockControlPlane, "us-east-1"))
	})

	t.Run("kill switch is case-insensitive", func(t *testing.T) {
		clearEndpointEnv(t)
		t.Setenv("AWS_ENDPOINT_URL", "https://env-global.example")
		t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "TRUE")
		provider := newTestProviderWithBaseURL(t, "")
		assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
	})
}

// TestWhitespaceOnlyBaseURLBehavesAsUnset pins that a whitespace-only BaseURL
// survives construction and resolves to the default endpoint.
func TestWhitespaceOnlyBaseURLBehavesAsUnset(t *testing.T) {
	clearEndpointEnv(t)
	provider := newTestProviderWithBaseURL(t, "  ")
	assert.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com", provider.endpointBase(schemas.Key{}, serviceBedrockRuntime, "us-east-1"))
}

func TestNewBedrockProviderRejectsInvalidBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{"no scheme", "not a url", true},
		{"ftp scheme", "ftp://x", true},
		{"scheme-relative", "//host", true},
		{"no host", "https://", true},
		{"query rejected", "https://host?x=1", true},
		{"bare query delimiter rejected", "https://host?", true},
		{"fragment rejected", "https://host#frag", true},
		{"bare fragment delimiter rejected", "https://host#", true},
		{"empty is ok", "", false},
		{"whitespace-only is ok", "  ", false},
		{"http with port ok", "http://127.0.0.1:1", false},
		{"trailing slash normalized", "https://vpce.example/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := &schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        tc.baseURL,
					DefaultRequestTimeoutInSeconds: 5,
				},
			}
			config.CheckAndSetDefaults()
			provider, err := NewBedrockProvider(config, noopLogger{})
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, provider)
			} else {
				require.NoError(t, err)
				require.NotNil(t, provider)
				assert.NotRegexp(t, "/$", provider.networkConfig.BaseURL, "constructor must trim trailing slashes")
			}
		})
	}
}
