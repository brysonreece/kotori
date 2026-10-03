package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// defaultBaseURL is the site's current address. It moves from time to time;
// --base-url or KOTORI_BASE_URL overrides it without a new release.
const defaultBaseURL = "https://anikototv.to"

const baseURLEnv = "KOTORI_BASE_URL"

// watchURL works out the page to load from the argument and the base URL.
// The argument is normally a series slug such as "dragon-ball-gxrfm", the
// last part of the series' address on the site. A slug, or a bare path such
// as "/watch/dragon-ball-gxrfm/ep-1", is always resolved against base. A full
// URL is used as given unless the base was set explicitly, in which case its
// scheme and host are replaced, so links to an old domain keep working.
func watchURL(arg, base string, explicit bool) (string, error) {
	baseURL, err := parseBaseURL(base)
	if err != nil {
		return "", err
	}
	arg = strings.TrimSpace(arg)
	switch first, _, hasSlash := strings.Cut(arg, "/"); {
	case arg == "":
		return "", errors.New("the series is empty")
	case !hasSlash:
		arg = "/watch/" + arg
	case !strings.Contains(arg, "://") && strings.Contains(first, "."):
		// A URL pasted without its scheme, such as "anikototv.to/watch/show".
		arg = "https://" + arg
	}
	target, err := url.Parse(arg)
	if err != nil {
		return "", fmt.Errorf("invalid series %q", arg)
	}
	if target.Host == "" {
		// A bare path, with or without its leading slash.
		target, err = url.Parse("/" + strings.TrimLeft(arg, "/"))
		if err != nil {
			return "", fmt.Errorf("invalid series %q", arg)
		}
		explicit = true
	}
	if explicit {
		target.Scheme, target.Host = baseURL.Scheme, baseURL.Host
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return "", fmt.Errorf("invalid series %q", arg)
	}
	return target.String(), nil
}

// parseBaseURL accepts a site address with or without a scheme, such as
// "anikototv.to" or "https://anikototv.to/".
func parseBaseURL(base string) (*url.URL, error) {
	base = strings.TrimSpace(base)
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("invalid base URL %q: expected something like %s", base, defaultBaseURL)
	}
	return u, nil
}
