package provisioning

import "testing"

func TestParseUFWStatus(t *testing.T) {
	active, allowed, _ := parseUFWStatus(`Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
443                        ALLOW       Anywhere
2222/tcp                   DENY        Anywhere
22/tcp (v6)                ALLOW       Anywhere (v6)
`)
	if !active || !allowed[22] || allowed[443] || allowed[2222] || len(allowed) != 1 {
		t.Fatalf("active=%v allowed=%v", active, allowed)
	}
	if active, _, _ := parseUFWStatus("Status: inactive\n"); active {
		t.Fatal("inactive reported as active")
	}
}

func TestParseSSHDConfigKeepsFirstValue(t *testing.T) {
	cfg := parseSSHDConfig("port 22\nport 2222\npasswordauthentication no\n")
	if cfg["port"] != "22" || cfg["passwordauthentication"] != "no" {
		t.Fatalf("cfg = %v", cfg)
	}
}
