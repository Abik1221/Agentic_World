package sdkstats

import (
	"net/http"
	"testing"
)

func TestCountryFromRequest_EthiopiaViaCloudflare(t *testing.T) {
	req, err := http.NewRequest("POST", "https://api.pyyol.com/v1/auth/signup", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("CF-IPCountry", "et")
	req.Header.Set("CF-Ray", "9f1a2b3c4d5e6f7a-ADD")
	req.Header.Set("CF-Connecting-IP", "196.188.1.10")
	got := CountryFromRequest(req)
	if got != "ET" {
		t.Fatalf("want ET (Ethiopia), got %q", got)
	}
}

func TestNormalizeCountry_Ethiopia(t *testing.T) {
	if got := NormalizeCountry("et"); got != "ET" {
		t.Fatalf("NormalizeCountry(et)=%q", got)
	}
	if got := NormalizeCountry("ET"); got != "ET" {
		t.Fatalf("NormalizeCountry(ET)=%q", got)
	}
}

func TestCountryFromRequest_SpoofedETIgnoredWithoutCFProvenance(t *testing.T) {
	req, err := http.NewRequest("POST", "https://api.pyyol.com/v1/auth/signup", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("CF-IPCountry", "ET") // no CF-Ray / CF-Connecting-IP
	req.RemoteAddr = "8.8.8.8:44321"
	got := CountryFromRequest(req)
	if got == "ET" {
		t.Fatal("spoofed CF-IPCountry=ET trusted without Cloudflare provenance")
	}
}
