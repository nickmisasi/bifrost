package bedrock

import (
	"fmt"
	"os"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// bedrockService identifies which AWS service endpoint a request targets.
type bedrockService int

const (
	serviceBedrockRuntime      bedrockService = iota // bedrock-runtime.{region}.{suffix}
	serviceBedrockControlPlane                       // bedrock.{region}.{suffix}
	serviceBedrockAgentRuntime                       // bedrock-agent-runtime.{region}.{suffix}
	serviceS3                                        // {bucket}.s3.{region}.{suffix}
	serviceBedrockMantle                             // bedrock-mantle.{region}.api.aws
	numBedrockServices
)

// defaultPartitionSuffix is the commercial-partition DNS suffix used when no
// override is configured.
const defaultPartitionSuffix = "amazonaws.com"

// endpointEnvVars maps each service to its AWS-standard service-specific
// endpoint environment variable (SDK convention: AWS_ENDPOINT_URL_<SERVICE_ID>).
var endpointEnvVars = [numBedrockServices]string{
	serviceBedrockRuntime:      "AWS_ENDPOINT_URL_BEDROCK_RUNTIME",
	serviceBedrockControlPlane: "AWS_ENDPOINT_URL_BEDROCK",
	serviceBedrockAgentRuntime: "AWS_ENDPOINT_URL_BEDROCK_AGENT_RUNTIME",
	serviceS3:                  "AWS_ENDPOINT_URL_S3",
	serviceBedrockMantle:       "AWS_ENDPOINT_URL_BEDROCK_MANTLE",
}

// loadEndpointEnvOverrides captures the AWS endpoint env vars once at provider
// construction so request paths never call os.Getenv. Honors the SDK-standard
// AWS_IGNORE_CONFIGURED_ENDPOINT_URLS kill switch.
func loadEndpointEnvOverrides() [numBedrockServices]string {
	var out [numBedrockServices]string
	if strings.EqualFold(os.Getenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"), "true") {
		return out
	}
	global := normalizeEndpointBase(os.Getenv("AWS_ENDPOINT_URL"))
	for svc, name := range endpointEnvVars {
		if v := normalizeEndpointBase(os.Getenv(name)); v != "" {
			out[svc] = v
		} else {
			out[svc] = global
		}
	}
	return out
}

// normalizeEndpointBase trims whitespace and trailing slashes so callers can
// unconditionally append "/path". Returns "" for empty input.
func normalizeEndpointBase(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// keyEndpointOverride returns the explicit per-service override from the key
// config, or "".
func keyEndpointOverride(key schemas.Key, service bedrockService) string {
	cfg := key.BedrockKeyConfig
	if cfg == nil || cfg.Endpoints == nil {
		return ""
	}
	switch service {
	case serviceBedrockRuntime:
		return normalizeEndpointBase(cfg.Endpoints.Runtime)
	case serviceBedrockControlPlane:
		return normalizeEndpointBase(cfg.Endpoints.ControlPlane)
	case serviceBedrockAgentRuntime:
		return normalizeEndpointBase(cfg.Endpoints.AgentRuntime)
	case serviceS3:
		return normalizeEndpointBase(cfg.Endpoints.S3)
	case serviceBedrockMantle:
		return normalizeEndpointBase(cfg.Endpoints.Mantle)
	}
	return ""
}

// dnsSuffixOverride returns the partition DNS suffix from the key config,
// or the commercial default.
func dnsSuffixOverride(key schemas.Key) string {
	if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.Endpoints != nil && key.BedrockKeyConfig.Endpoints.DNSSuffix != "" {
		return strings.Trim(key.BedrockKeyConfig.Endpoints.DNSSuffix, ".")
	}
	return defaultPartitionSuffix
}

// endpointBase resolves the "https://host[:port]" prefix (no trailing slash)
// for a non-S3 service. Precedence: key.Endpoints.<service> >
// NetworkConfig.BaseURL (runtime only) > AWS_ENDPOINT_URL_* env >
// key.Endpoints.DNSSuffix > commercial default. An empty result is impossible;
// the fallback always yields today's hostname, so default behavior is unchanged.
func (provider *BedrockProvider) endpointBase(key schemas.Key, service bedrockService, region string) string {
	if v := keyEndpointOverride(key, service); v != "" {
		return v
	}
	if service == serviceBedrockRuntime && provider.networkConfig.BaseURL != "" {
		return provider.networkConfig.BaseURL
	}
	if v := provider.envEndpoints[service]; v != "" {
		return v
	}
	switch service {
	case serviceBedrockRuntime:
		return fmt.Sprintf("https://bedrock-runtime.%s.%s", region, dnsSuffixOverride(key))
	case serviceBedrockControlPlane:
		return fmt.Sprintf("https://bedrock.%s.%s", region, dnsSuffixOverride(key))
	case serviceBedrockAgentRuntime:
		return fmt.Sprintf("https://bedrock-agent-runtime.%s.%s", region, dnsSuffixOverride(key))
	case serviceBedrockMantle:
		return fmt.Sprintf("https://bedrock-mantle.%s.api.aws", region)
	}
	// serviceS3 is handled by s3BucketBase (bucket placement differs).
	panic("bedrock: endpointBase called with S3 service; use s3BucketBase")
}

// s3BucketBase resolves the base URL for S3 operations on bucket.
// Default / DNSSuffix: virtual-hosted style "https://{bucket}.s3.{region}.{suffix}".
// Explicit override (key config or env): path-style "{endpoint}/{bucket}", so a
// single service endpoint (VPCE, MinIO, localstack) serves every bucket.
func (provider *BedrockProvider) s3BucketBase(key schemas.Key, region, bucket string) string {
	if v := keyEndpointOverride(key, serviceS3); v != "" {
		return v + "/" + bucket
	}
	if v := provider.envEndpoints[serviceS3]; v != "" {
		return v + "/" + bucket
	}
	return fmt.Sprintf("https://%s.s3.%s.%s", bucket, region, dnsSuffixOverride(key))
}
