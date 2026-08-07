// End-to-end tests proving the provider's real request paths honor
// NetworkConfig.BaseURL and per-key endpoint overrides. Unlike the
// redirectTransport harness in transport_test.go, these tests point the
// provider at httptest servers purely through configuration.

package bedrock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedRequest struct {
	method string
	host   string
	uri    string
	header http.Header
}

// requestRecorder captures requests seen by an httptest server for later assertions.
type requestRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (rr *requestRecorder) record(r *http.Request) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.reqs = append(rr.reqs, recordedRequest{
		method: r.Method,
		host:   r.Host,
		uri:    r.URL.RequestURI(),
		header: r.Header.Clone(),
	})
}

func (rr *requestRecorder) all() []recordedRequest {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]recordedRequest(nil), rr.reqs...)
}

// newRecordingServer returns an httptest server that records every request and
// responds 200 with the given content type and body.
func newRecordingServer(t *testing.T, contentType, body string) (*httptest.Server, *requestRecorder) {
	t.Helper()
	rec := &requestRecorder{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, rec
}

// hostOf strips the http:// scheme from an httptest server URL, leaving host:port.
func hostOf(ts *httptest.Server) string {
	return strings.TrimPrefix(ts.URL, "http://")
}

func TestCompleteRequestHonorsBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	ts, rec := newRecordingServer(t, "application/json", `{}`)
	provider := newTestProviderWithBaseURL(t, ts.URL)

	body, _, _, bifrostErr := provider.completeRequest(testBedrockCtx(), []byte(`{"messages":[]}`), "model-id/converse", testBedrockKey(), "model-id")
	require.Nil(t, bifrostErr)
	assert.Equal(t, `{}`, string(body))

	reqs := rec.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].method)
	assert.Equal(t, "/model/model-id/converse", reqs[0].uri)
	assert.Equal(t, hostOf(ts), reqs[0].host)
}

func TestMakeStreamingRequestHonorsBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	ts, rec := newRecordingServer(t, "application/vnd.amazon.eventstream", "")
	provider := newTestProviderWithBaseURL(t, ts.URL)

	resp, bifrostErr := provider.makeStreamingRequest(testBedrockCtx(), []byte(`{}`), testBedrockKey(), testConverseStreamModel, "converse-stream")
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	resp.Body.Close()

	reqs := rec.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].method)
	assert.Equal(t, "/model/"+testConverseStreamModel+"/converse-stream", reqs[0].uri)
	assert.Equal(t, hostOf(ts), reqs[0].host)
}

func TestListModelsUsesControlPlaneEndpointNotBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	tsBase, recBase := newRecordingServer(t, "application/json", `{}`)
	tsControl, recControl := newRecordingServer(t, "application/json", `{"modelSummaries":[]}`)
	tsMantle, recMantle := newRecordingServer(t, "application/json", `{"data":[]}`)

	provider := newTestProviderWithBaseURL(t, tsBase.URL)
	key := testBedrockKey()
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpointsConfig{
		ControlPlane: tsControl.URL,
		Mantle:       tsMantle.URL,
	}

	resp, bifrostErr := provider.listModelsByKey(testBedrockCtx(), key, &schemas.BifrostListModelsRequest{})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	assert.Empty(t, recBase.all(), "BaseURL (bedrock-runtime) must not receive control-plane traffic")

	controlReqs := recControl.all()
	require.Len(t, controlReqs, 1)
	assert.Equal(t, http.MethodGet, controlReqs[0].method)
	assert.True(t, strings.HasPrefix(controlReqs[0].uri, "/foundation-models"), "got %q", controlReqs[0].uri)
	assert.Equal(t, hostOf(tsControl), controlReqs[0].host)

	mantleReqs := recMantle.all()
	require.Len(t, mantleReqs, 1)
	assert.Equal(t, http.MethodGet, mantleReqs[0].method)
	assert.Equal(t, "/v1/models", mantleReqs[0].uri)
	assert.Equal(t, hostOf(tsMantle), mantleReqs[0].host)
}

func TestAgentRuntimeHonorsEndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	ts, rec := newRecordingServer(t, "application/json", `{}`)
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockKey()
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpointsConfig{AgentRuntime: ts.URL}

	body, _, _, bifrostErr := provider.completeAgentRuntimeRequest(testBedrockCtx(), []byte(`{}`), "/rerank", key)
	require.Nil(t, bifrostErr)
	assert.Equal(t, `{}`, string(body))

	reqs := rec.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].method)
	assert.Equal(t, "/rerank", reqs[0].uri)
	assert.Equal(t, hostOf(ts), reqs[0].host)
}

// testBedrockSigV4Key returns a key with static SigV4 credentials (no bearer
// Value) so request paths take the signing branch, plus the given overrides.
func testBedrockSigV4Key(ep *schemas.BedrockEndpointsConfig) schemas.Key {
	return schemas.Key{
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
			SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: ep,
		},
	}
}

// signedHeadersOf extracts the SignedHeaders list from an Authorization header.
func signedHeadersOf(t *testing.T, auth string) []string {
	t.Helper()
	_, signedPart, found := strings.Cut(auth, "SignedHeaders=")
	require.True(t, found, "no SignedHeaders in Authorization: %q", auth)
	signedHeadersStr, _, _ := strings.Cut(signedPart, ",")
	return strings.Split(signedHeadersStr, ";")
}

// TestFileUploadHonorsS3EndpointOverride drives the real S3 PUT path with a
// path-style endpoint override: the bucket moves into the path, each key
// segment is percent-escaped, and SigV4 signs the custom host with the "s3"
// service in the credential scope.
func TestFileUploadHonorsS3EndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	ts, rec := newRecordingServer(t, "application/octet-stream", "")
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockSigV4Key(&schemas.BedrockEndpointsConfig{S3: ts.URL})

	resp, bifrostErr := provider.FileUpload(testBedrockCtx(), key, &schemas.BifrostFileUploadRequest{
		File:        []byte("batch data"),
		Filename:    "a b/c#d",
		ExtraParams: map[string]interface{}{"s3_bucket": "test-bucket"},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)
	assert.Equal(t, "s3://test-bucket/a b/c#d", resp.ID)

	reqs := rec.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPut, reqs[0].method)
	assert.Equal(t, "/test-bucket/a%20b/c%23d", reqs[0].uri, "path-style bucket plus per-segment key escaping")
	assert.Equal(t, hostOf(ts), reqs[0].host)

	auth := reqs[0].header.Get("Authorization")
	assert.Regexp(t, `^AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/\d{8}/us-east-1/s3/aws4_request`, auth,
		"credential scope must keep the configured region and the s3 service")
	assert.Contains(t, signedHeadersOf(t, auth), "host")
}

// TestFileListHonorsS3EndpointOverride locks the {endpoint}/{bucket}/?params
// URI shape through the real ListObjectsV2 request path.
func TestFileListHonorsS3EndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`
	ts, rec := newRecordingServer(t, "application/xml", xmlBody)
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockSigV4Key(&schemas.BedrockEndpointsConfig{S3: ts.URL})

	resp, bifrostErr := provider.FileList(testBedrockCtx(), []schemas.Key{key}, &schemas.BifrostFileListRequest{
		ExtraParams: map[string]interface{}{"s3_bucket": "test-bucket"},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	reqs := rec.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodGet, reqs[0].method)
	assert.Equal(t, "/test-bucket/?list-type=2&prefix=", reqs[0].uri)
	assert.Equal(t, hostOf(ts), reqs[0].host)
	assert.Regexp(t, `/us-east-1/s3/aws4_request`, reqs[0].header.Get("Authorization"))
}

// TestDialGuardBlocksPrivateEndpointOverride proves the provider's net/http
// transport enforces the private-network policy at dial time: an endpoint
// override pointing at an RFC 1918 address is rejected before any packet is
// sent when allow_private_network is false (the default in these tests). The
// IP-literal target keeps this hermetic — no DNS, no connection attempt.
//
// Note the rest of this file's httptest servers bind 127.0.0.1: loopback is
// always allowed by the policy (mirroring ConfigureDialer for fasthttp
// providers), so those tests need no allow_private_network opt-in and double
// as proof that loopback traffic flows under the guard. The
// allow_private_network=true permit path is covered hermetically by
// TestHTTPPolicyDialContext_PrivatePolicy in providers/utils.
func TestDialGuardBlocksPrivateEndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockKey()
	// TEST-NET-style private target; port 9 (discard) would hang or refuse if
	// the guard failed to reject pre-dial.
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpointsConfig{Runtime: "http://10.255.255.1:9"}

	_, _, _, bifrostErr := provider.completeRequest(testBedrockCtx(), []byte(`{"messages":[]}`), "model-id/converse", key, "model-id")
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Error)
	assert.Contains(t, bifrostErr.Error.Error.Error(), "private IP")
}

// TestSigV4SigningOverCustomHost locks in that SigV4 signing follows the
// overridden URL: the signed host is the custom host (including its
// non-default port) and the credential scope keeps the configured region and
// the "bedrock" service name — never anything derived from the hostname.
func TestSigV4SigningOverCustomHost(t *testing.T) {
	clearEndpointEnv(t)
	ts, rec := newRecordingServer(t, "application/json", `{}`)
	provider := newTestProviderWithBaseURL(t, ts.URL)

	// No key.Value → completeRequest takes the SigV4 path with static credentials.
	key := testBedrockSigV4Key(nil)

	_, _, _, bifrostErr := provider.completeRequest(testBedrockCtx(), []byte(`{}`), "model-id/converse", key, "model-id")
	require.Nil(t, bifrostErr)

	reqs := rec.all()
	require.Len(t, reqs, 1)

	auth := reqs[0].header.Get("Authorization")
	assert.Regexp(t, `^AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/\d{8}/us-east-1/bedrock/aws4_request`, auth,
		"credential scope must keep the configured region and bedrock service")
	assert.Contains(t, signedHeadersOf(t, auth), "host")

	// The wire Host equals the custom host with its non-default port, so the
	// signed value and the sent value match.
	assert.Equal(t, hostOf(ts), reqs[0].host)
}
