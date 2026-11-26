package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func feedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Port() != "" && u.Port() != "443") || strings.ContainsAny(raw, "# \r\n\t") ||
		strings.Contains(u.Host, "%") {
		return nil, errors.New("expected a public HTTPS feed URL")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicFeedIP(ip) {
		return nil, errors.New("feed address is not public")
	}
	return u, nil
}

var globalIPv6 = netip.MustParsePrefix("2000::/3")

var specialFeedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicFeedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && !globalIPv6.Contains(ip)) {
		return false
	}
	for _, prefix := range specialFeedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func newFeedClient() *http.Client {
	transport := &http.Transport{
		DialContext:            dialPublicFeed,
		TLSHandshakeTimeout:    5 * time.Second,
		IdleConnTimeout:        30 * time.Second,
		MaxIdleConns:           8,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport, // No environment proxy: every connection passes through dialPublicFeed.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many feed redirects")
			}
			_, err := feedURL(req.URL.String())
			return err
		},
	}
}

func dialPublicFeed(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	err = errors.New("feed host has no public addresses")
	dialer := net.Dialer{Timeout: 3 * time.Second}
	for _, ip := range ips {
		if !publicFeedIP(ip) {
			continue
		}
		// Dial the checked IP directly so DNS cannot change it between validation and use.
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}
