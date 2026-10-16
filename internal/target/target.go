// SPDX-License-Identifier: Apache-2.0
// Package target maps a public page URL to a fixed, separately configured origin.
package target

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
)

type Page struct {
	Site   string `json:"site"`
	URL    string `json:"url"`
	Origin string `json:"origin"`
}

func Root(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("site/origin must be an http(s) root without credentials, query or fragment")
	}
	if p := u.Port(); p != "" {
		if _, err := net.LookupPort("tcp", p); err != nil {
			return nil, errors.New("invalid port")
		}
	}
	normalize(u)
	u.Path = ""
	return u, nil
}
func normalize(u *url.URL) {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = host
		if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
	}
}
func Map(site, origin, raw string) (Page, error) {
	s, err := Root(site)
	if err != nil {
		return Page{}, err
	}
	o, err := Root(origin)
	if err != nil {
		return Page{}, err
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.Opaque != "" {
		return Page{}, errors.New("invalid page URL")
	}
	if !u.IsAbs() {
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return Page{}, errors.New("page must be absolute or root-relative")
		}
		u = s.ResolveReference(u)
	}
	normalize(u)
	if u.Scheme != s.Scheme || u.Host != s.Host {
		return Page{}, errors.New("page URL is outside the configured public site")
	}
	u.Fragment = ""
	u.RawFragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	upstream := *u
	upstream.Scheme = o.Scheme
	upstream.Host = o.Host
	return Page{Site: s.String(), URL: u.String(), Origin: upstream.String()}, nil
}
func (p Page) Validate() error {
	u, err := url.Parse(p.Origin)
	if err != nil || u == nil {
		return errors.New("invalid origin page")
	}
	root := *u
	root.Path = ""
	root.RawPath = ""
	root.RawQuery = ""
	root.ForceQuery = false
	root.Fragment = ""
	root.RawFragment = ""
	mapped, err := Map(p.Site, root.String(), p.URL)
	if err != nil {
		return err
	}
	if mapped != p {
		return errors.New("noncanonical page identity")
	}
	return nil
}
func (p Page) Key() string {
	sum := sha256.Sum256([]byte("rendercache:v1\x00" + p.Site + "\x00" + p.URL + "\x00" + p.Origin))
	return hex.EncodeToString(sum[:])
}
func (p Page) PublicURL(originURL string) (string, error) {
	original, _ := url.Parse(p.Origin)
	u, err := url.Parse(originURL)
	if err != nil || u == nil || u.User != nil {
		return "", errors.New("invalid final URL")
	}
	normalize(u)
	if u.Scheme != original.Scheme || u.Host != original.Host {
		return "", errors.New("navigation left origin")
	}
	s, _ := Root(p.Site)
	u.Scheme = s.Scheme
	u.Host = s.Host
	u.Fragment = ""
	return u.String(), nil
}
