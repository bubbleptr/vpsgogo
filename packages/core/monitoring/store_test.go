package monitoring

import (
	"bytes"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleAt(at time.Time, cpu float64) Sample {
	return Sample{
		Time: at, CPU: cpu, MemUsed: uint64(cpu) * 10, MemTotal: 1000,
		NetRx: cpu * 2, NetTx: cpu * 3, Load1: cpu / 10, Load5: cpu / 20, Load15: cpu / 40,
		Uptime: float64(at.Unix()), TCPConns: int(cpu),
		Disks: []Disk{{Mount: "/", Used: uint64(cpu), Total: 100}},
	}
}

func times(ss []Sample) []time.Time {
	out := make([]time.Time, len(ss))
	for i, s := range ss {
		out[i] = s.Time
	}
	return out
}

func TestStoreTiers(t *testing.T) {
	s := NewStore()
	const n = 9000 // 25h of 10s samples
	for i := 0; i < n; i++ {
		s.Add(sampleAt(t0.Add(time.Duration(i)*10*time.Second), float64(i%100)))
	}
	newest := t0.Add((n - 1) * 10 * time.Second)
	got := s.Since(time.Time{})

	// Fine samples reach back exactly 24h from the newest; older history is
	// one sample per minute, and the two tiers neither overlap nor repeat.
	oldestFine := newest.Add(-24 * time.Hour)
	var coarse, fine []Sample
	for i, x := range got {
		if i > 0 && !x.Time.After(got[i-1].Time) {
			t.Fatalf("not strictly ascending at %d: %v after %v", i, x.Time, got[i-1].Time)
		}
		if x.Time.Before(oldestFine) {
			coarse = append(coarse, x)
		} else {
			fine = append(fine, x)
		}
	}
	if len(fine) != 24*360+1 || !fine[0].Time.Equal(oldestFine) {
		t.Errorf("fine tier: %d samples from %v, want %d from %v", len(fine), fine[0].Time, 24*360+1, oldestFine)
	}
	for i, x := range coarse {
		if want := t0.Add(time.Duration(i) * time.Minute); !x.Time.Equal(want) {
			t.Fatalf("coarse[%d].Time = %v, want %v", i, x.Time, want)
		}
	}
	// oldest fine is 00:59:50 into the run; minute 59 is partly fine, so the
	// coarse tier stops at minute 58.
	if len(coarse) != 59 {
		t.Errorf("coarse tier has %d samples, want 59", len(coarse))
	}
}

func TestStoreCoarseAverage(t *testing.T) {
	s := NewStore()
	for i, cpu := range []float64{10, 20, 30, 40, 50, 60} {
		x := sampleAt(t0.Add(time.Duration(i)*10*time.Second), cpu)
		s.Add(x)
	}
	s.Add(sampleAt(t0.Add(time.Minute), 99))
	s.Add(sampleAt(t0.Add(25*time.Hour), 1)) // pushes both minutes out of the fine tier

	got := s.Since(time.Time{})
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 2 coarse + 1 fine: %v", len(got), times(got))
	}
	avg := got[0]
	last := sampleAt(t0.Add(50*time.Second), 60)
	want := Sample{
		Time: t0, CPU: 35, MemUsed: 350, MemTotal: 1000,
		NetRx: 70, NetTx: 105, Load1: 3.5, Load5: 1.75, Load15: 0.875,
		Uptime: last.Uptime, TCPConns: 60, Disks: last.Disks,
	}
	if !avg.Time.Equal(want.Time) {
		t.Errorf("Time = %v, want start of minute %v", avg.Time, want.Time)
	}
	avg.Time = want.Time
	if !reflect.DeepEqual(avg, want) {
		t.Errorf("minute average:\n got %+v\nwant %+v", avg, want)
	}
	if !got[1].Time.Equal(t0.Add(time.Minute)) || got[1].CPU != 99 {
		t.Errorf("second minute = %v cpu %v", got[1].Time, got[1].CPU)
	}
}

func TestStoreCoarsePrunedAfter7Days(t *testing.T) {
	s := NewStore()
	s.Add(sampleAt(t0, 1))
	s.Add(sampleAt(t0.Add(48*time.Hour), 2))
	s.Add(sampleAt(t0.Add(8*24*time.Hour), 3))
	got := times(s.Since(time.Time{}))
	want := []time.Time{t0.Add(48 * time.Hour), t0.Add(8 * 24 * time.Hour)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Since = %v, want %v", got, want)
	}
}

func TestStoreSinceIsStrictlyAfter(t *testing.T) {
	s := NewStore()
	for i := 0; i < 5; i++ {
		s.Add(sampleAt(t0.Add(time.Duration(i)*10*time.Second), 1))
	}
	got := times(s.Since(t0.Add(20 * time.Second)))
	want := []time.Time{t0.Add(30 * time.Second), t0.Add(40 * time.Second)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Since = %v, want %v", got, want)
	}
	if got := s.Since(t0.Add(40 * time.Second)); len(got) != 0 {
		t.Errorf("Since(newest) = %v, want none", times(got))
	}
}

func TestStoreLatest(t *testing.T) {
	s := NewStore()
	if _, ok := s.Latest(); ok {
		t.Fatal("empty store has a latest sample")
	}
	s.Add(sampleAt(t0, 1))
	s.Add(sampleAt(t0.Add(10*time.Second), 2))
	if x, ok := s.Latest(); !ok || x.CPU != 2 {
		t.Errorf("Latest = %v, %v", x.CPU, ok)
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	// The partial minute must survive a restart: samples added after Load
	// average with the ones saved before it.
	before := []Sample{sampleAt(t0, 10), sampleAt(t0.Add(time.Minute), 20), sampleAt(t0.Add(time.Minute+10*time.Second), 40)}
	after := []Sample{sampleAt(t0.Add(time.Minute+20*time.Second), 60), sampleAt(t0.Add(2*time.Minute), 1), sampleAt(t0.Add(25*time.Hour), 2)}

	whole := NewStore()
	saved := NewStore()
	for _, x := range before {
		whole.Add(x)
		saved.Add(x)
	}
	var buf bytes.Buffer
	if err := saved.Save(&buf); err != nil {
		t.Fatal(err)
	}
	loaded := NewStore()
	loaded.Add(sampleAt(t0.Add(-time.Hour), 77)) // replaced by Load
	if err := loaded.Load(&buf); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Since(time.Time{}), whole.Since(time.Time{})) {
		t.Fatalf("after Load:\n got %v\nwant %v", times(loaded.Since(time.Time{})), times(whole.Since(time.Time{})))
	}
	for _, x := range after {
		whole.Add(x)
		loaded.Add(x)
	}
	got, want := loaded.Since(time.Time{}), whole.Since(time.Time{})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after more samples:\n got %+v\nwant %+v", got, want)
	}
	if got[1].CPU != 40 { // minute 1: (20+40+60)/3
		t.Errorf("minute 1 CPU = %v, want 40", got[1].CPU)
	}
}

func TestStoreLoadRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "not json", "[]", `"x"`, "{}", `{"version":1,"fine":"x"}`} {
		s := NewStore()
		s.Add(sampleAt(t0, 5))
		if err := s.Load(strings.NewReader(in)); err == nil {
			t.Errorf("Load(%q) succeeded", in)
		}
		if x, ok := s.Latest(); !ok || x.CPU != 5 {
			t.Errorf("Load(%q) failed but changed the store", in)
		}
	}
}

func TestStoreConcurrentUse(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			s.Add(sampleAt(t0.Add(time.Duration(i)*10*time.Second), 1))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			for j := range s.Since(time.Time{}) {
				_ = j
			}
			s.Latest()
			_ = s.Save(&bytes.Buffer{})
		}
	}()
	wg.Wait()
}

func TestStoreSinceReturnsCopies(t *testing.T) {
	s := NewStore()
	s.Add(sampleAt(t0, 1))
	got := s.Since(time.Time{})
	got[0].CPU = 1000
	got[0].Disks[0].Used = 1000
	again := s.Since(time.Time{})
	if again[0].CPU != 1 || again[0].Disks[0].Used != 1 {
		t.Error("mutating the result of Since changed the store")
	}
}
