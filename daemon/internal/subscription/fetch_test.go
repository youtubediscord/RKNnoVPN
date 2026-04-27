package subscription

import (
	"errors"
	"testing"

	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
)

func TestRefreshResultResponseExposesStableRPCFields(t *testing.T) {
	result := RefreshResult{
		Source: SubscriptionSource{
			ProviderKey: "provider",
			URL:         "https://example.com/sub",
		},
		Subscription: profiledoc.Subscription{
			ProviderKey:   "provider",
			URL:           "https://example.com/sub",
			LastFetchedAt: 123,
		},
		Nodes:         []profiledoc.Node{{ID: "node-1"}, {ID: "node-2"}},
		ParseFailures: 3,
		Merge:         map[string]int{"added": 2},
	}

	response := result.Response()
	if response.Imported != 2 || response.ParseFailures != 3 {
		t.Fatalf("unexpected response counts: %#v", response)
	}
	if response.Source.ProviderKey != "provider" || response.Subscription.LastFetchedAt != 123 {
		t.Fatalf("unexpected response metadata: %#v", response)
	}
	if response.Merge["added"] != 2 {
		t.Fatalf("unexpected merge stats: %#v", response.Merge)
	}
}

func TestClassifyError(t *testing.T) {
	if got := ClassifyError("", errors.New("missing")); got != ErrorInvalidParams {
		t.Fatalf("empty URL should be invalid params, got %s", got)
	}
	if got := ClassifyError("ftp://example.com/sub", errors.New("bad scheme")); got != ErrorInvalidParams {
		t.Fatalf("bad URL should be invalid params, got %s", got)
	}
	if got := ClassifyError("https://example.com/sub", ErrNoSupportedNodes); got != ErrorConfig {
		t.Fatalf("unsupported nodes should be config error, got %s", got)
	}
	if got := ClassifyError("https://example.com/sub", errors.New("network down")); got != ErrorInternal {
		t.Fatalf("fetch failure should be internal error, got %s", got)
	}
}
