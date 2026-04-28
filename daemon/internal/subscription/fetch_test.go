package subscription

import (
	"errors"
	"strings"
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
		RejectedNodes: []profiledoc.RejectedSubscriptionNode{{Server: "127.0.0.1", Port: 10808, Code: "subscription_local_endpoint"}},
		ParseFailures: 3,
		Merge:         map[string]int{"added": 2},
	}

	response := result.Response()
	if response.Imported != 2 || response.ParseFailures != 3 || response.Rejected != 1 {
		t.Fatalf("unexpected response counts: %#v", response)
	}
	if len(response.RejectedNodes) != 1 || response.RejectedNodes[0].Server != "127.0.0.1" {
		t.Fatalf("unexpected rejected node metadata: %#v", response.RejectedNodes)
	}
	if response.Source.ProviderKey != "provider" || response.Subscription.LastFetchedAt != 123 {
		t.Fatalf("unexpected response metadata: %#v", response)
	}
	if response.Merge["added"] != 2 {
		t.Fatalf("unexpected merge stats: %#v", response.Merge)
	}
}

func TestPreviewReportsRejectedSubscriptionEndpoints(t *testing.T) {
	client := NewClient(FetcherFunc(func(rawURL string) (FetchResult, error) {
		return FetchResult{
			Status: 200,
			Body: strings.Join([]string{
				"vless://00000000-0000-0000-0000-000000000000@example.com:443#public",
				"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:10808#local",
			}, "\n"),
		}, nil
	}))

	preview, err := client.Preview("https://example.com/sub", profiledoc.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 1 || preview.Rejected != 1 {
		t.Fatalf("unexpected preview result: %#v", preview)
	}
	if preview.RejectedNodes[0].Code != "subscription_local_endpoint" {
		t.Fatalf("unexpected rejection metadata: %#v", preview.RejectedNodes)
	}
}

func TestPreviewKeepsRejectedMetadataWhenOnlyRejectedAndParseFailures(t *testing.T) {
	client := NewClient(FetcherFunc(func(rawURL string) (FetchResult, error) {
		return FetchResult{
			Status: 200,
			Body: strings.Join([]string{
				"not-a-link",
				"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:10808#local",
			}, "\n"),
		}, nil
	}))

	preview, err := client.Preview("https://example.com/sub", profiledoc.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 0 || preview.ParseFailures != 1 || preview.Rejected != 1 {
		t.Fatalf("preview should preserve both parse and rejected counts: %#v", preview)
	}
	if len(preview.RejectedNodes) != 1 || preview.RejectedNodes[0].Server != "127.0.0.1" {
		t.Fatalf("preview lost rejected endpoint metadata: %#v", preview.RejectedNodes)
	}
}

func TestApplyRefreshRejectsAllRejectedSubscriptionEndpoints(t *testing.T) {
	client := NewClient(FetcherFunc(func(rawURL string) (FetchResult, error) {
		return FetchResult{
			Status: 200,
			Body:   "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:10808#local",
		}, nil
	}))

	result, err := client.ApplyRefresh("https://example.com/sub", profiledoc.Document{})
	if !errors.Is(err, ErrNoSupportedNodes) {
		t.Fatalf("expected no supported nodes error, got result=%#v err=%v", result, err)
	}
	if len(result.RejectedNodes) != 1 {
		t.Fatalf("expected rejected endpoint in refresh result: %#v", result)
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
