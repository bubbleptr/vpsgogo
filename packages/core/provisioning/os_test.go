package provisioning

import "testing"

func TestSupportedOS(t *testing.T) {
	cases := []struct {
		name, release string
		ok            bool
	}{
		{"debian 12", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n", true},
		{"debian 13", "ID=debian\nVERSION_ID=\"13\"\n", true},
		{"debian 11", "ID=debian\nVERSION_ID=\"11\"\n", false},
		{"ubuntu 22.04", "ID=ubuntu\nVERSION_ID=\"22.04\"\n", true},
		{"ubuntu 24.04", "ID=ubuntu\nVERSION_ID=\"24.04\"\n", true},
		{"ubuntu 20.04", "ID=ubuntu\nVERSION_ID=\"20.04\"\n", false},
		{"debian testing has no VERSION_ID", "ID=debian\n", false},
		{"centos", "ID=\"centos\"\nVERSION_ID=\"9\"\n", false},
		{"alpine", "ID=alpine\nVERSION_ID=3.20.0\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os := parseOSRelease(c.release)
			if err := os.supported(); (err == nil) != c.ok {
				t.Fatalf("%+v supported() = %v, want ok=%v", os, err, c.ok)
			}
		})
	}
}
