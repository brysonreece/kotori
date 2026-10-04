// Package fetch is the small HTTP client shared by the site scraper and the
// HLS downloader.
package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// UserAgent is sent with every request.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// Client performs GET requests with browser-like defaults.
type Client struct {
	HTTP *http.Client
	// Debugf, when set, receives one line per request.
	Debugf func(format string, args ...any)
}

// Response is a fully read response body plus the URL it was finally served
// from, after redirects.
type Response struct {
	Body []byte
	URL  *url.URL
}

// StatusError is returned for a response with a non-2xx status.
type StatusError struct {
	URL    string
	Code   int
	Status string
	// RetryAfter is the server's Retry-After delay, or zero if it gave none.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: %s", e.URL, e.Status)
}

// New returns a Client with a cookie jar and a per-request timeout.
func New() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{HTTP: &http.Client{Jar: jar, Timeout: 2 * time.Minute}}
}

// Get fetches rawURL, treating any non-2xx status as an error.
func (c *Client) Get(ctx context.Context, rawURL string, headers map[string]string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if c.Debugf != nil {
		c.Debugf("GET %s -> %s (%d bytes)", rawURL, resp.Status, len(body))
	}
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{
			URL:        rawURL,
			Code:       resp.StatusCode,
			Status:     resp.Status,
			RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return &Response{Body: body, URL: resp.Request.URL}, nil
}

// retryAfter reads a Retry-After header, which is either a number of seconds
// or a date. It returns zero when the header is missing, unreadable or
// already past.
func retryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if seconds, err := strconv.Atoi(header); err == nil {
		return max(time.Duration(seconds)*time.Second, 0)
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(at.Sub(now), 0)
	}
	return 0
}

// GetJSON fetches rawURL and decodes the body into v.
func (c *Client) GetJSON(ctx context.Context, rawURL string, headers map[string]string, v any) error {
	resp, err := c.Get(ctx, rawURL, headers)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Body, v); err != nil {
		return fmt.Errorf("GET %s: unexpected response: %w", rawURL, err)
	}
	return nil
}
