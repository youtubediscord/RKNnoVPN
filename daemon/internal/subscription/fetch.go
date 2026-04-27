package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
)

const maxBodyBytes = 4 * 1024 * 1024

var ErrNoSupportedNodes = errors.New("subscription contains no supported nodes")

type FetchResult struct {
	Status  int
	Body    string
	Headers map[string]string
}

type Fetcher interface {
	FetchURL(rawURL string) (FetchResult, error)
}

type FetcherFunc func(rawURL string) (FetchResult, error)

func (f FetcherFunc) FetchURL(rawURL string) (FetchResult, error) {
	return f(rawURL)
}

type Client struct {
	Fetcher Fetcher
	Now     func() time.Time
}

type SubscriptionSource struct {
	ProviderKey string `json:"providerKey"`
	URL         string `json:"url"`
}

type ManualNode struct {
	Node profiledoc.Node `json:"node"`
}

type SubscriptionNode struct {
	Node   profiledoc.Node    `json:"node"`
	Source SubscriptionSource `json:"source"`
	Stale  bool               `json:"stale"`
}

type PreviewResult struct {
	Source        SubscriptionSource      `json:"source"`
	Subscription  profiledoc.Subscription `json:"subscription"`
	Nodes         []profiledoc.Node       `json:"nodes"`
	Added         int                     `json:"added"`
	Updated       int                     `json:"updated"`
	Unchanged     int                     `json:"unchanged"`
	Stale         int                     `json:"stale"`
	ParseFailures int                     `json:"parseFailures"`
	FetchStatus   int                     `json:"-"`
	FetchHeaders  map[string]string       `json:"-"`
}

type RefreshResult struct {
	Source        SubscriptionSource      `json:"source"`
	Profile       profiledoc.Document     `json:"profile"`
	Subscription  profiledoc.Subscription `json:"subscription"`
	Nodes         []profiledoc.Node       `json:"nodes"`
	Merge         map[string]int          `json:"merge"`
	ParseFailures int                     `json:"parseFailures"`
	FetchStatus   int                     `json:"-"`
	FetchHeaders  map[string]string       `json:"-"`
}

func NewClient(fetcher Fetcher) Client {
	if fetcher == nil {
		fetcher = FetcherFunc(FetchURL)
	}
	return Client{Fetcher: fetcher, Now: time.Now}
}

func (c Client) Preview(rawURL string, current profiledoc.Document) (PreviewResult, error) {
	source, nodes, sub, failures, fetched, err := c.fetchAndParse(rawURL)
	if err != nil {
		return PreviewResult{Source: source, FetchStatus: fetched.Status, FetchHeaders: fetched.Headers}, err
	}
	_, stats := profiledoc.MergeSubscriptionNodes(current, sub, nodes)
	return PreviewResult{
		Source:        source,
		Subscription:  sub,
		Nodes:         nodes,
		Added:         stats["added"],
		Updated:       stats["updated"],
		Unchanged:     stats["unchanged"],
		Stale:         stats["stale"],
		ParseFailures: failures,
		FetchStatus:   fetched.Status,
		FetchHeaders:  fetched.Headers,
	}, nil
}

func (c Client) ApplyRefresh(rawURL string, current profiledoc.Document) (RefreshResult, error) {
	source, nodes, sub, failures, fetched, err := c.fetchAndParse(rawURL)
	if err != nil {
		return RefreshResult{Source: source, FetchStatus: fetched.Status, FetchHeaders: fetched.Headers}, err
	}
	next, stats := profiledoc.MergeSubscriptionNodes(current, sub, nodes)
	replaced := false
	for i, existing := range next.Subscriptions {
		if existing.ProviderKey == sub.ProviderKey {
			sub.Name = existing.Name
			next.Subscriptions[i] = sub
			replaced = true
			break
		}
	}
	if !replaced {
		next.Subscriptions = append(next.Subscriptions, sub)
	}
	return RefreshResult{
		Source:        source,
		Profile:       next,
		Subscription:  sub,
		Nodes:         nodes,
		Merge:         stats,
		ParseFailures: failures,
		FetchStatus:   fetched.Status,
		FetchHeaders:  fetched.Headers,
	}, nil
}

func (c Client) fetchAndParse(rawURL string) (SubscriptionSource, []profiledoc.Node, profiledoc.Subscription, int, FetchResult, error) {
	source, err := NewSubscriptionSource(rawURL)
	if err != nil {
		return source, nil, profiledoc.Subscription{}, 0, FetchResult{}, err
	}
	if c.Fetcher == nil {
		c.Fetcher = FetcherFunc(FetchURL)
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	fetched, err := c.Fetcher.FetchURL(source.URL)
	if err != nil {
		return source, nil, profiledoc.Subscription{}, 0, fetched, err
	}
	nodes, sub, failures := profiledoc.ParseSubscription(fetched.Body, fetched.Headers, source.URL, now.UnixMilli())
	sub.ProviderKey = source.ProviderKey
	sub.URL = source.URL
	for i := range nodes {
		nodes[i].Source.ProviderKey = source.ProviderKey
		nodes[i].Source.URL = source.URL
	}
	if len(nodes) == 0 && failures > 0 {
		return source, nil, sub, failures, fetched, ErrNoSupportedNodes
	}
	return source, nodes, sub, failures, fetched, nil
}

func FetchURL(rawURL string) (FetchResult, error) {
	var result FetchResult
	source, err := NewSubscriptionSource(rawURL)
	if err != nil {
		return result, err
	}

	req, err := http.NewRequest(http.MethodGet, source.URL, nil)
	if err != nil {
		return result, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "RKNnoVPN-subscription/2.0")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = fetchDialContext
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("subscription fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return result, fmt.Errorf("subscription read failed: %w", err)
	}
	if len(body) > maxBodyBytes {
		return result, fmt.Errorf("subscription response is too large")
	}

	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FetchResult{Status: resp.StatusCode, Headers: headers}, fmt.Errorf("subscription fetch returned HTTP %d", resp.StatusCode)
	}

	return FetchResult{
		Status:  resp.StatusCode,
		Body:    string(body),
		Headers: headers,
	}, nil
}

func ValidateFetchURL(rawURL string) error {
	_, err := NewSubscriptionSource(rawURL)
	return err
}

func NewSubscriptionSource(rawURL string) (SubscriptionSource, error) {
	var source SubscriptionSource
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return source, fmt.Errorf("url is required")
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return source, fmt.Errorf("invalid URL: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return source, fmt.Errorf("subscription URL scheme must be http or https")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return source, fmt.Errorf("subscription URL host is required")
	}
	if IsDisallowedHost(host) {
		return source, fmt.Errorf("subscription URL host is local or private")
	}
	source.URL = parsed.String()
	source.ProviderKey = profiledoc.ProviderKeyFor(source.URL)
	return source, nil
}

func fetchDialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = address
		port = ""
	}
	if IsDisallowedHost(host) {
		return nil, fmt.Errorf("subscription URL host is local or private")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, resolved := range ips {
		if IsDisallowedIP(resolved.IP) {
			return nil, fmt.Errorf("subscription URL resolved to local or private address")
		}
	}
	var dialer net.Dialer
	var lastErr error
	for _, resolved := range ips {
		dialAddress := address
		if port != "" {
			dialAddress = net.JoinHostPort(resolved.IP.String(), port)
		}
		conn, err := dialer.DialContext(ctx, network, dialAddress)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("subscription URL host did not resolve")
}

func IsDisallowedHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return IsDisallowedIP(ip)
	}
	return false
}

func IsDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
}
