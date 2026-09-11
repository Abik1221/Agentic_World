package sdkstats

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/agent-arena/arena/internal/middleware"
	"github.com/phuslu/iploc"
)

// Cloudflare / RFC 1918 sentinels that look like ISO codes but are not a country.
// Stored as UnknownCountry so the per-country table stays a real geography.
var notACountry = map[string]struct{}{
	"XX": {}, // unknown
	"ZZ": {}, // reserved / unknown
	"T1": {}, // Tor
	"A1": {}, // anonymous proxy
	"A2": {}, // satellite
	"O1": {}, // other
	"--": {}, // iploc: address not in the database
}

// CountryFromRequest resolves the install to a 2-letter ISO country.
//
// The package used to read ONLY Cloudflare's CF-IPCountry header. Production sits
// behind nginx on a VPS, so that header is never set and every ping was stored as
// XX — which the admin console renders as "Unknown". The comment claimed we GeoIP
// the request; this is that lookup.
//
// Order:
//  1. A CDN country header, but only when a provenance header proves the request
//     actually came through that CDN. Otherwise CF-IPCountry is just another
//     client-settable field and anyone could inflate a country.
//  2. GeoIP of middleware.ClientIP (trusted X-Forwarded-For hop). Private /
//     loopback / unparseable addresses stay UnknownCountry.
func CountryFromRequest(r *http.Request) string {
	if behindCloudflare(r) {
		if c := NormalizeCountry(r.Header.Get("CF-IPCountry")); c != UnknownCountry {
			return c
		}
	}
	if c := NormalizeCountry(r.Header.Get("CloudFront-Viewer-Country")); c != UnknownCountry {
		// CloudFront overwrites this; a client hitting origin directly would not
		// normally send it, and a spoof still has to match a public IP GeoIP below
		// if we fall through. Prefer it when present and valid.
		if r.Header.Get("CloudFront-Viewer-ASN") != "" || r.Header.Get("X-Amz-Cf-Id") != "" {
			return c
		}
	}
	return CountryFromIP(middleware.ClientIP(r))
}

func behindCloudflare(r *http.Request) bool {
	return r.Header.Get("CF-Connecting-IP") != "" || r.Header.Get("CF-Ray") != ""
}

// CountryFromIP maps a dotted IP to a country. Never stores the IP.
func CountryFromIP(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		if host, _, splitErr := net.SplitHostPort(ip); splitErr == nil {
			addr, err = netip.ParseAddr(host)
		}
	}
	if err != nil {
		return UnknownCountry
	}
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsMulticast() {
		return UnknownCountry
	}
	return NormalizeCountry(iploc.IPCountry(addr))
}

// NormalizeCountry upper-cases and validates a 2-letter ISO country code; anything
// else (including Cloudflare's XX/T1 sentinels) becomes UnknownCountry.
func NormalizeCountry(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	if _, skip := notACountry[c]; skip {
		return UnknownCountry
	}
	if len(c) != 2 {
		return UnknownCountry
	}
	for i := 0; i < 2; i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return UnknownCountry
		}
	}
	return c
}
