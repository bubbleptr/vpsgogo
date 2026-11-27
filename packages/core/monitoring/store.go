package monitoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"
	"time"
)

const (
	fineKeep   = 24 * time.Hour
	coarseKeep = 7 * 24 * time.Hour
	storeVer   = 1
)

// Store keeps samples in two tiers: every sample for 24h, and one average per
// UTC minute for 7 days. It is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	fine   []Sample
	coarse []Sample
	minute *bucket // the minute still collecting samples
}

// bucket accumulates the samples of one minute. It is saved as sums so a
// restart in the middle of a minute does not skew its average.
type bucket struct {
	Start    time.Time `json:"start"`
	N        int       `json:"n"`
	CPU      float64   `json:"cpu"`
	NetRx    float64   `json:"netRx"`
	NetTx    float64   `json:"netTx"`
	Load1    float64   `json:"load1"`
	Load5    float64   `json:"load5"`
	Load15   float64   `json:"load15"`
	MemUsed  float64   `json:"memUsed"`
	SwapUsed float64   `json:"swapUsed"`
	Last     Sample    `json:"last"`
}

func (b *bucket) add(x Sample) {
	b.N++
	b.CPU += x.CPU
	b.NetRx += x.NetRx
	b.NetTx += x.NetTx
	b.Load1 += x.Load1
	b.Load5 += x.Load5
	b.Load15 += x.Load15
	b.MemUsed += float64(x.MemUsed)
	b.SwapUsed += float64(x.SwapUsed)
	b.Last = x
}

// sample is the minute's average: rates and gauges averaged, totals, disks,
// uptime and connections taken from the last sample.
func (b *bucket) sample() Sample {
	n := float64(b.N)
	s := b.Last
	s.Time = b.Start
	s.CPU = b.CPU / n
	s.NetRx = b.NetRx / n
	s.NetTx = b.NetTx / n
	s.Load1 = b.Load1 / n
	s.Load5 = b.Load5 / n
	s.Load15 = b.Load15 / n
	s.MemUsed = uint64(math.Round(b.MemUsed / n))
	s.SwapUsed = uint64(math.Round(b.SwapUsed / n))
	s.Disks = slices.Clone(b.Last.Disks)
	return s
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{} }

// Add appends a sample; samples are expected in time order.
func (s *Store) Add(x Sample) {
	x.Disks = slices.Clone(x.Disks)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.fine = append(s.fine, x)
	start := x.Time.UTC().Truncate(time.Minute)
	if s.minute != nil && start.After(s.minute.Start) {
		s.coarse = append(s.coarse, s.minute.sample())
		s.minute = nil
	}
	if s.minute == nil {
		s.minute = &bucket{Start: start}
	}
	s.minute.add(x)

	s.fine = pruneBefore(s.fine, x.Time.Add(-fineKeep))
	s.coarse = pruneBefore(s.coarse, x.Time.Add(-coarseKeep))
}

// pruneBefore drops the leading samples older than cutoff.
func pruneBefore(ss []Sample, cutoff time.Time) []Sample {
	i := 0
	for i < len(ss) && ss[i].Time.Before(cutoff) {
		i++
	}
	if i == 0 {
		return ss
	}
	return slices.Delete(ss, 0, i)
}

// Since returns the samples after t in ascending time order: minute averages
// for history older than the fine tier, then every sample. A minute that
// overlaps the oldest fine sample is left to the fine tier.
func (s *Store) Since(t time.Time) []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Sample
	for _, x := range s.coarse {
		if len(s.fine) > 0 && x.Time.Add(time.Minute).After(s.fine[0].Time) {
			break
		}
		if x.Time.After(t) {
			out = append(out, copySample(x))
		}
	}
	for _, x := range s.fine {
		if x.Time.After(t) {
			out = append(out, copySample(x))
		}
	}
	return out
}

// Latest returns the newest sample.
func (s *Store) Latest() (Sample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.fine) == 0 {
		return Sample{}, false
	}
	return copySample(s.fine[len(s.fine)-1]), true
}

func copySample(x Sample) Sample {
	x.Disks = slices.Clone(x.Disks)
	return x
}

// storeFile is the JSON form of a Store.
type storeFile struct {
	Version int      `json:"version"`
	Fine    []Sample `json:"fine"`
	Coarse  []Sample `json:"coarse"`
	Minute  *bucket  `json:"minute,omitempty"`
}

// Save writes the store as JSON.
func (s *Store) Save(w io.Writer) error {
	s.mu.Lock()
	f := storeFile{Version: storeVer, Fine: s.fine, Coarse: s.coarse, Minute: s.minute}
	b, err := json.Marshal(f)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// Load replaces the store's contents with what Save wrote. On error the
// store is unchanged.
func (s *Store) Load(r io.Reader) error {
	var f storeFile
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return fmt.Errorf("monitoring: load store: %w", err)
	}
	if f.Version != storeVer {
		return fmt.Errorf("monitoring: load store: unsupported version %d", f.Version)
	}
	if f.Minute != nil && f.Minute.N <= 0 {
		return errors.New("monitoring: load store: empty minute bucket")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fine, s.coarse, s.minute = f.Fine, f.Coarse, f.Minute
	return nil
}
