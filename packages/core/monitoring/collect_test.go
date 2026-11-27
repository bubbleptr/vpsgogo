package monitoring

import (
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

// fakeStatfs reports fixed usage per mount; mounts listed in fail return an error.
func fakeStatfs(fail ...string) func(string) (uint64, uint64, error) {
	return func(mount string) (uint64, uint64, error) {
		for _, f := range fail {
			if f == mount {
				return 0, 0, errors.New("permission denied")
			}
		}
		return uint64(len(mount)) * 1000, 100000, nil
	}
}

func snap(name string) string { return filepath.Join("testdata", name) }

func TestCollectFirstCallIsBaseline(t *testing.T) {
	c := &Collector{Root: snap("snap1"), Statfs: fakeStatfs()}
	_, ok, err := c.Collect(t0)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("first Collect returned ok=true; rates need a previous reading")
	}
}

func TestCollectTwoSnapshots(t *testing.T) {
	c := &Collector{Root: snap("snap1"), Statfs: fakeStatfs()}
	if _, _, err := c.Collect(t0); err != nil {
		t.Fatal(err)
	}
	c.Root = snap("snap2")
	s, ok, err := c.Collect(t0.Add(10 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("second Collect returned ok=false")
	}
	// cpu deltas over user..steal: total 2000, idle+iowait 1500 → 25%.
	// guest (+300) is already inside user and must not count twice.
	if math.Abs(s.CPU-25) > 1e-9 {
		t.Errorf("CPU = %v, want 25", s.CPU)
	}
	// eth0 +100000/+20000, wg0 +10000/+10000 over 10s; lo's +500000 ignored.
	if s.NetRx != 11000 || s.NetTx != 3000 {
		t.Errorf("NetRx, NetTx = %v, %v, want 11000, 3000", s.NetRx, s.NetTx)
	}
	if want := uint64(2014288-1210000) * 1024; s.MemUsed != want || s.MemTotal != 2014288*1024 {
		t.Errorf("mem = %d/%d, want %d/%d", s.MemUsed, s.MemTotal, want, 2014288*1024)
	}
	if s.SwapUsed != 48572*1024 || s.SwapTotal != 1048572*1024 {
		t.Errorf("swap = %d/%d", s.SwapUsed, s.SwapTotal)
	}
	if s.Load1 != 0.52 || s.Load5 != 0.31 || s.Load15 != 0.25 {
		t.Errorf("load = %v %v %v", s.Load1, s.Load5, s.Load15)
	}
	if s.Uptime != 86410.5 {
		t.Errorf("uptime = %v", s.Uptime)
	}
	// tcp: 3 ESTABLISHED (listen and time-wait rows ignored); tcp6: 2.
	if s.TCPConns != 5 {
		t.Errorf("TCPConns = %d, want 5", s.TCPConns)
	}
	if !s.Time.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("Time = %v", s.Time)
	}
}

func TestCollectDisks(t *testing.T) {
	tests := []struct {
		name string
		fail []string
		want []string
	}{
		{
			// tmpfs, proc, overlay, squashfs and mounts under /run, /snap are skipped;
			// /var/lib/docker/volumes/data repeats /dev/vda1; /development is not /dev.
			name: "filtering",
			want: []string{"/", "/boot/efi", "/development", "/mnt/my data"},
		},
		{
			name: "statfs error skips one mount",
			fail: []string{"/boot/efi"},
			want: []string{"/", "/development", "/mnt/my data"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Collector{Root: snap("snap1"), Statfs: fakeStatfs(tt.fail...)}
			if _, _, err := c.Collect(t0); err != nil {
				t.Fatal(err)
			}
			s, ok, err := c.Collect(t0.Add(10 * time.Second))
			if err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			var got []string
			for _, d := range s.Disks {
				got = append(got, d.Mount)
				if d.Used != uint64(len(d.Mount))*1000 || d.Total != 100000 {
					t.Errorf("%s: used/total = %d/%d", d.Mount, d.Used, d.Total)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mounts = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCollectMissingTCP6(t *testing.T) {
	c := &Collector{Root: snap("snap1"), Statfs: fakeStatfs()} // snap1 has no tcp6
	if _, _, err := c.Collect(t0); err != nil {
		t.Fatal(err)
	}
	s, _, err := c.Collect(t0.Add(10 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.TCPConns != 1 {
		t.Errorf("TCPConns = %d, want 1", s.TCPConns)
	}
}

func TestCollectCounterReset(t *testing.T) {
	// After a reboot or interface reset counters go backwards: rate 0, not negative.
	c := &Collector{Root: snap("snap2"), Statfs: fakeStatfs()}
	if _, _, err := c.Collect(t0); err != nil {
		t.Fatal(err)
	}
	c.Root = snap("snap1")
	s, ok, err := c.Collect(t0.Add(10 * time.Second))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if s.NetRx != 0 || s.NetTx != 0 {
		t.Errorf("NetRx, NetTx = %v, %v, want 0, 0", s.NetRx, s.NetTx)
	}
	if s.CPU < 0 || s.CPU > 100 {
		t.Errorf("CPU = %v outside 0–100", s.CPU)
	}
}

func TestCollectMissingProcIsError(t *testing.T) {
	c := &Collector{Root: t.TempDir(), Statfs: fakeStatfs()}
	if _, _, err := c.Collect(t0); err == nil {
		t.Fatal("Collect on an empty root succeeded")
	}
}
