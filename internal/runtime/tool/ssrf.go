package tools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// privateNetworks defines CIDR ranges considered internal/private.
var privateNetworks = []string{
	"127.0.0.0/8",    // loopback
	"10.0.0.0/8",     // RFC 1918
	"172.16.0.0/12",  // RFC 1918
	"192.168.0.0/16", // RFC 1918
	"169.254.0.0/16", // link-local
	"::1/128",        // IPv6 loopback
	"fc00::/7",       // IPv6 unique local
	"fe80::/10",      // IPv6 link-local
}

var parsedPrivateNets []*net.IPNet

type urlValidationOptions struct {
	AllowExplicitLoopback bool
}

func init() {
	for _, cidr := range privateNetworks {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			parsedPrivateNets = append(parsedPrivateNets, ipNet)
		}
	}
}

// isPrivateIP returns true if the IP address is in a private/internal range.
func isPrivateIP(ip net.IP) bool {
	for _, n := range parsedPrivateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// validateURLNotInternal checks that a URL does not point to an internal/private
// network address. This prevents SSRF attacks that could access cloud metadata
// endpoints, localhost services, or internal network resources.
func validateURLNotInternal(rawURL string) error {
	return validateURLAccess(rawURL, urlValidationOptions{})
}

// validateBrowserURL allows browser automation against explicit local dev
// loopback URLs while still blocking metadata, link-local, and private networks.
func validateBrowserURL(rawURL string) error {
	return validateURLAccess(rawURL, urlValidationOptions{AllowExplicitLoopback: true})
}

func validateURLAccess(rawURL string, opts urlValidationOptions) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host")
	}

	// Block common metadata hostnames
	lower := strings.ToLower(host)
	if lower == "metadata.google.internal" || lower == "metadata" {
		return fmt.Errorf("access to internal metadata endpoint is blocked")
	}

	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		// Resolve hostname to IP addresses
		resolved, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("cannot resolve hostname %q — blocking to prevent SSRF", host)
		}
		ips = resolved
	}

	for _, ip := range ips {
		if isPrivateIP(ip) {
			if opts.AllowExplicitLoopback && ip.IsLoopback() && isExplicitLoopbackHost(host) {
				continue
			}
			return fmt.Errorf("access to internal address %s (%s) is blocked", host, ip)
		}
	}

	return nil
}

func isExplicitLoopbackHost(host string) bool {
	lower := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
