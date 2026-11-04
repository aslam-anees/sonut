package providerhealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/config"
)

func TestProbeUsesProviderSpecificModelEndpointAndRedactsCredentials(t *testing.T) {
	const secret = "sk-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("authorization = %q, want bearer credential", got)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result := Probe(context.Background(), Options{
		Profile: config.ProviderProfile{
			ProviderKind: config.ProviderKindOpenAICompatible,
			BaseURL:      server.URL + "/v1",
			APIKey:       secret,
		},
		Connectivity: true,
	})
	if result.Status != StatusPass {
		t.Fatalf("status = %s, want pass: %#v", result.Status, result)
	}
	if check := result.PrimaryCheck(); check == nil || check.Status != StatusPass {
		t.Fatalf("primary check = %#v, want pass", result.PrimaryCheck())
	}
	if strings.Contains(result.PrimaryCheck().Message, secret) {
		t.Fatalf("health message leaked credential: %q", result.PrimaryCheck().Message)
	}
}

func TestProbeClassifiesAuthRateLimitAndUnavailable(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		want     Status
		category Category
	}{
		{name: "auth", status: http.StatusUnauthorized, want: StatusFail, category: CategoryAuth},
		{name: "rate limit", status: http.StatusTooManyRequests, want: StatusWarn, category: CategoryRateLimit},
		{name: "unavailable", status: http.StatusBadGateway, want: StatusFail, category: CategoryNone},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(testCase.status)
			}))
			defer server.Close()
			result := Probe(context.Background(), Options{
				Profile:      config.ProviderProfile{BaseURL: server.URL + "/v1"},
				Connectivity: true,
			})
			check := result.PrimaryCheck()
			if check == nil || check.Status != testCase.want || check.Category != testCase.category {
				t.Fatalf("check = %#v, want status=%s category=%d", check, testCase.want, testCase.category)
			}
		})
	}
}

func TestProbeConfigurationModeDoesNotPerformNetworkIO(t *testing.T) {
	result := Probe(context.Background(), Options{
		Profile: config.ProviderProfile{BaseURL: "not a URL"},
	})
	if result.Status != StatusFail {
		t.Fatalf("status = %s, want fail", result.Status)
	}
	if len(result.Checks) != 1 || result.Checks[0].ID != "provider.configuration" {
		t.Fatalf("checks = %#v, want provider.configuration", result.Checks)
	}
}
