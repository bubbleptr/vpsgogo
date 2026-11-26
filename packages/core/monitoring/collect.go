package monitoring

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Collector reads samples from /proc. It keeps the previous reading so it can
// turn counters into rates; it is not safe for concurrent use.
type Collector struct {
	Root   string                                             // filesystem root to read proc from; "" means "/"
	Statfs func(mount string) (used, total uint64, err error) // nil: the real statfs (Linux only)

	prev *reading
}

// reading is the counter state rates are computed from.
type reading struct {
	at       time.Time
	cpuTotal uint64
	cpuIdle  uint64
	rx, tx   uint64
}

// diskFS are the filesystem types that hold real disks.
var diskFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "vfat": true, "exfat": true,
}

// virtualRoots are trees whose mounts are never user disks.
var virtualRoots = []string{"/proc", "/sys", "/dev", "/run", "/snap"}

// Collect reads one sample at now. The first call only stores a baseline for
// CPU and network rates and returns ok=false.
func (c *Collector) Collect(now time.Time) (s Sample, ok bool, err error) {
	cur := reading{at: now}
	if cur.cpuTotal, cur.cpuIdle, err = c.readCPU(); err != nil {
		return Sample{}, false, err
	}
	if cur.rx, cur.tx, err = c.readNet(); err != nil {
		return Sample{}, false, err
	}
	s = Sample{Time: now}
	if err = c.readMem(&s); err != nil {
		return Sample{}, false, err
	}
	if err = c.readLoad(&s); err != nil {
		return Sample{}, false, err
	}
	if err = c.readUptime(&s); err != nil {
		return Sample{}, false, err
	}
	if s.TCPConns, err = c.readTCP(); err != nil {
		return Sample{}, false, err
	}
	if s.Disks, err = c.readDisks(); err != nil {
		return Sample{}, false, err
	}

	prev := c.prev
	c.prev = &cur
	if prev == nil {
		return Sample{}, false, nil
	}
	if dt, di := delta(prev.cpuTotal, cur.cpuTotal), delta(prev.cpuIdle, cur.cpuIdle); dt > 0 && di <= dt {
		s.CPU = float64(dt-di) / float64(dt) * 100
	}
	if secs := now.Sub(prev.at).Seconds(); secs > 0 {
		s.NetRx = float64(delta(prev.rx, cur.rx)) / secs
		s.NetTx = float64(delta(prev.tx, cur.tx)) / secs
	}
	return s, true, nil
}

// delta is b-a, or 0 when a counter went backwards.
func delta(a, b uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}

func (c *Collector) read(name string) ([]byte, error) {
	root := c.Root
	if root == "" {
		root = "/"
	}
	return os.ReadFile(filepath.Join(root, name))
}

func lines(b []byte) []string {
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func parseUint(name, s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func parseFloat(name, s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

// readCPU returns the total and idle jiffies of the aggregate cpu line.
// Only user..steal count: guest and guest_nice are already inside user and nice.
func (c *Collector) readCPU() (total, idle uint64, err error) {
	const name = "proc/stat"
	b, err := c.read(name)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range lines(b) {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "cpu" {
			continue
		}
		if len(f) < 5 {
			return 0, 0, fmt.Errorf("%s: short cpu line %q", name, line)
		}
		for i := 1; i < len(f) && i <= 8; i++ {
			v, err := parseUint(name, f[i])
			if err != nil {
				return 0, 0, err
			}
			total += v
			if i == 4 || i == 5 { // idle, iowait
				idle += v
			}
		}
		return total, idle, nil
	}
	return 0, 0, fmt.Errorf("%s: no cpu line", name)
}

func (c *Collector) readMem(s *Sample) error {
	const name = "proc/meminfo"
	b, err := c.read(name)
	if err != nil {
		return err
	}
	kb := map[string]uint64{}
	for _, line := range lines(b) {
		key, rest, found := strings.Cut(line, ":")
		f := strings.Fields(rest)
		if !found || len(f) == 0 {
			continue
		}
		switch key {
		case "MemTotal", "MemAvailable", "SwapTotal", "SwapFree":
			if kb[key], err = parseUint(name, f[0]); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"MemTotal", "MemAvailable", "SwapTotal", "SwapFree"} {
		if _, ok := kb[key]; !ok {
			return fmt.Errorf("%s: no %s", name, key)
		}
	}
	s.MemTotal = kb["MemTotal"] * 1024
	s.MemUsed = delta(kb["MemAvailable"], kb["MemTotal"]) * 1024
	s.SwapTotal = kb["SwapTotal"] * 1024
	s.SwapUsed = delta(kb["SwapFree"], kb["SwapTotal"]) * 1024
	return nil
}

// readNet sums received and sent bytes over all interfaces but lo.
func (c *Collector) readNet() (rx, tx uint64, err error) {
	const name = "proc/net/dev"
	b, err := c.read(name)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range lines(b) {
		iface, rest, found := strings.Cut(line, ":")
		if !found {
			continue // the two header lines
		}
		if strings.TrimSpace(iface) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return 0, 0, fmt.Errorf("%s: short line %q", name, line)
		}
		r, err := parseUint(name, f[0])
		if err != nil {
			return 0, 0, err
		}
		t, err := parseUint(name, f[8])
		if err != nil {
			return 0, 0, err
		}
		rx += r
		tx += t
	}
	return rx, tx, nil
}

func (c *Collector) readLoad(s *Sample) error {
	const name = "proc/loadavg"
	b, err := c.read(name)
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return fmt.Errorf("%s: malformed %q", name, b)
	}
	for i, dst := range []*float64{&s.Load1, &s.Load5, &s.Load15} {
		if *dst, err = parseFloat(name, f[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) readUptime(s *Sample) error {
	const name = "proc/uptime"
	b, err := c.read(name)
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return fmt.Errorf("%s: empty", name)
	}
	s.Uptime, err = parseFloat(name, f[0])
	return err
}

// readTCP counts ESTABLISHED sockets in tcp and tcp6; a missing tcp6 (IPv6
// disabled) counts as none.
func (c *Collector) readTCP() (int, error) {
	n := 0
	for _, name := range []string{"proc/net/tcp", "proc/net/tcp6"} {
		b, err := c.read(name)
		if name == "proc/net/tcp6" && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		for i, line := range lines(b) {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue // header
			}
			f := strings.Fields(line)
			if len(f) < 4 {
				return 0, fmt.Errorf("%s: short line %q", name, line)
			}
			if f[3] == "01" {
				n++
			}
		}
	}
	return n, nil
}

// readDisks lists real disk mounts, one per device, sorted by mount path.
// A mount whose statfs fails is left out.
func (c *Collector) readDisks() ([]Disk, error) {
	const name = "proc/self/mounts"
	b, err := c.read(name)
	if err != nil {
		return nil, err
	}
	statfs := c.Statfs
	if statfs == nil {
		statfs = sysStatfs
	}
	seen := map[string]bool{}
	var disks []Disk
	for _, line := range lines(b) {
		f := strings.Fields(line)
		if len(f) < 3 {
			return nil, fmt.Errorf("%s: short line %q", name, line)
		}
		dev, mount, fstype := unescape(f[0]), unescape(f[1]), f[2]
		if !diskFS[fstype] || underVirtual(mount) || seen[dev] {
			continue
		}
		seen[dev] = true
		used, total, err := statfs(mount)
		if err != nil {
			continue
		}
		disks = append(disks, Disk{Mount: mount, Used: used, Total: total})
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Mount < disks[j].Mount })
	return disks, nil
}

func underVirtual(mount string) bool {
	for _, p := range virtualRoots {
		if mount == p || strings.HasPrefix(mount, p+"/") {
			return true
		}
	}
	return false
}

// unescape decodes the octal escapes (\040 for space and so on) the kernel
// uses in /proc/self/mounts.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var out bytes.Buffer
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
