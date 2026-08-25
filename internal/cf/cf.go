// Package cf wraps the official Cloudflare Go SDK (v7) for the small surface
// this tool uses: zones, DNS records and cache purge. It isolates the
// SDK's generated style (Field wrappers) from the rest of the program.
package cf

import (
	"context"
	"fmt"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/cache"
	"github.com/cloudflare/cloudflare-go/v7/dns"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/zones"
)

// Zone is the subset of a Cloudflare zone this tool persists.
type Zone struct {
	ID   string
	Name string
}

// Record is the subset of a Cloudflare DNS record this tool persists.
type Record struct {
	ID       string
	ZoneID   string
	Type     string
	Name     string // FQDN, e.g. "www.example.com"
	Content  string
	Proxied  bool
	TTL      int // 1 == automatic
	Priority int // MX/URI only
}

// Client is a thin wrapper around the Cloudflare SDK.
type Client struct {
	api *cloudflare.Client
}

// New creates a client authenticated with an API token (recommended) and
// verifies the token works before returning.
func New(ctx context.Context, token string) (*Client, error) {
	c := cloudflare.NewClient(option.WithAPIToken(token))
	if _, err := c.User.Tokens.Verify(ctx); err != nil {
		return nil, fmt.Errorf("cloudflare auth: %w", err)
	}
	return &Client{api: c}, nil
}

// NewWithAPIKey creates a client with a legacy global API key — dev fallback
// until the account is migrated to a scoped token. There is no cheap
// key-based verify endpoint; the first real call validates the credentials.
func NewWithAPIKey(email, key string) *Client {
	return &Client{api: cloudflare.NewClient(option.WithAPIKey(key), option.WithAPIEmail(email))}
}

// ListZones returns every zone on the account (all pages).
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	iter := c.api.Zones.ListAutoPaging(ctx, zones.ZoneListParams{})
	var out []Zone
	for iter.Next() {
		z := iter.Current()
		out = append(out, Zone{ID: z.ID, Name: z.Name})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	return out, nil
}

// ListRecords returns every DNS record of a zone (all pages).
func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	params := dns.RecordListParams{ZoneID: cloudflare.F(zoneID)}
	iter := c.api.DNS.Records.ListAutoPaging(ctx, params)
	var out []Record
	for iter.Next() {
		r := iter.Current()
		out = append(out, Record{
			ID:       r.ID,
			ZoneID:   zoneID,
			Type:     string(r.Type),
			Name:     r.Name,
			Content:  r.Content,
			Proxied:  r.Proxied,
			TTL:      int(r.TTL),
			Priority: int(r.Priority),
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list records for zone %s: %w", zoneID, err)
	}
	return out, nil
}

// UpdateARecord changes the content of an existing A record, preserving its
// proxied state and TTL.
func (c *Client) UpdateARecord(ctx context.Context, r Record, content string) error {
	_, err := c.api.DNS.Records.Update(ctx, r.ID, dns.RecordUpdateParams{
		ZoneID: cloudflare.F(r.ZoneID),
		Body: dns.ARecordParam{
			Name:    cloudflare.F(r.Name),
			TTL:     cloudflare.F(dns.TTL(r.TTL)),
			Type:    cloudflare.F(dns.ARecordType("A")),
			Content: cloudflare.F(content),
			Proxied: cloudflare.F(r.Proxied),
		},
	})
	if err != nil {
		return fmt.Errorf("update A record %s in zone %s: %w", r.ID, r.ZoneID, err)
	}
	return nil
}

// CreateARecord adds a new A record (proxied, auto TTL by default) and
// returns the created record including its Cloudflare ID.
func (c *Client) CreateARecord(ctx context.Context, zoneID, name, content string, proxied bool, ttl int) (Record, error) {
	res, err := c.api.DNS.Records.New(ctx, dns.RecordNewParams{
		ZoneID: cloudflare.F(zoneID),
		Body: dns.ARecordParam{
			Name:    cloudflare.F(name),
			TTL:     cloudflare.F(dns.TTL(ttl)),
			Type:    cloudflare.F(dns.ARecordType("A")),
			Content: cloudflare.F(content),
			Proxied: cloudflare.F(proxied),
		},
	})
	if err != nil {
		return Record{}, fmt.Errorf("create A record %s in zone %s: %w", name, zoneID, err)
	}
	return Record{
		ID:      res.ID,
		ZoneID:  zoneID,
		Type:    "A",
		Name:    res.Name,
		Content: res.Content,
		Proxied: res.Proxied,
		TTL:     int(res.TTL),
	}, nil
}

// PurgeCache purges all cached content for a zone.
func (c *Client) PurgeCache(ctx context.Context, zoneID string) error {
	_, err := c.api.Cache.Purge(ctx, cache.CachePurgeParams{
		ZoneID: cloudflare.F(zoneID),
		Body:   cache.CachePurgeParamsBodyCachePurgeEverything{},
	})
	if err != nil {
		return fmt.Errorf("purge zone %s: %w", zoneID, err)
	}
	return nil
}
