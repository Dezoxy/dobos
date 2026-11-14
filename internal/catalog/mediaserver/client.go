package mediaserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"flodni/internal/catalog"
)

// Client refreshes a media server library section. Plex addresses one section and does
// nothing without an ID, so callers pass whatever they have and let the client skip the
// call when it cannot make it.
type Client interface {
	RefreshLibrary(ctx context.Context, sectionID int) error
}

// New creates the Plex client.
func New(url, token string) Client {
	return &PlexClient{
		http:  catalog.NewClient(15 * time.Second),
		URL:   url,
		Token: token,
	}
}

// PlexClient implements Client for Plex Media Server.
type PlexClient struct {
	http  *http.Client
	URL   string
	Token string
}

// RefreshLibrary triggers a Plex library scan. A section ID of 0 means "not configured",
// the documented way to leave the refresh off.
func (c *PlexClient) RefreshLibrary(ctx context.Context, sectionID int) error {
	if c.URL == "" || c.Token == "" || sectionID <= 0 {
		return nil
	}

	urlStr := fmt.Sprintf("%s/library/sections/%d/refresh?X-Plex-Token=%s", c.URL, sectionID, c.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}

	resp, err := catalog.Do(ctx, c.http, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("plex refresh: status %d", resp.StatusCode)
	}

	return nil
}
