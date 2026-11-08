package stats

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// maxRelativeError bounds the error of a quantile relative to the exact
// nearest-rank value.
const maxRelativeError = 1.0 / subBucketCount

func TestBucketsAreContiguous(t *testing.T) {
	last := bucketIndex(math.MaxInt64)
	if got := bucketHighest(last); got != math.MaxInt64 {
		t.Fatalf("bucketHighest(%d) = %d, want MaxInt64", last, got)
	}
	if bucketLowest(0) != 0 {
		t.Fatalf("bucketLowest(0) = %d, want 0", bucketLowest(0))
	}
	for i := range last {
		if bucketHighest(i)+1 != bucketLowest(i+1) {
			t.Fatalf("bucket %d ends at %d but bucket %d starts at %d", i, bucketHighest(i), i+1, bucketLowest(i+1))
		}
	}
}

func TestBucketIndexContainsValue(t *testing.T) {
	values := []uint64{0, 1, subBucketCount - 1, subBucketCount, subBucketCount + 1, 255, 256, 257, 1000, 1 << 20, 1<<20 + 12345, math.MaxInt64}
	r := rand.New(rand.NewPCG(1, 2))
	for range 10000 {
		values = append(values, r.Uint64N(math.MaxInt64))
	}
	for _, v := range values {
		i := bucketIndex(v)
		lo, hi := bucketLowest(i), bucketHighest(i)
		if v < lo || v > hi {
			t.Fatalf("value %d is outside its bucket %d [%d, %d]", v, i, lo, hi)
		}
		if float64(hi-lo) > float64(lo)*maxRelativeError {
			t.Fatalf("bucket %d [%d, %d] is wider than %g of its start", i, lo, hi, maxRelativeError)
		}
	}
}

func TestHistogramEmpty(t *testing.T) {
	var h Histogram
	if h.Count() != 0 || h.Min() != 0 || h.Max() != 0 || h.Mean() != 0 || h.Quantile(0.99) != 0 {
		t.Fatalf("empty histogram: count=%d min=%v max=%v mean=%v p99=%v", h.Count(), h.Min(), h.Max(), h.Mean(), h.Quantile(0.99))
	}
}

func TestHistogramExactStatistics(t *testing.T) {
	var h Histogram
	for _, d := range []time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond, 10 * time.Millisecond} {
		h.Record(d)
	}
	if h.Count() != 4 {
		t.Errorf("Count() = %d, want 4", h.Count())
	}
	if h.Min() != time.Millisecond {
		t.Errorf("Min() = %v, want 1ms", h.Min())
	}
	if h.Max() != 10*time.Millisecond {
		t.Errorf("Max() = %v, want 10ms", h.Max())
	}
	if h.Mean() != 4*time.Millisecond {
		t.Errorf("Mean() = %v, want 4ms", h.Mean())
	}
	if got := h.Quantile(1); got != 10*time.Millisecond {
		t.Errorf("Quantile(1) = %v, want the maximum", got)
	}
}

func TestHistogramSmallValuesAreExact(t *testing.T) {
	var h Histogram
	for v := range time.Duration(100) {
		h.Record(v + 1)
	}
	for _, tc := range []struct {
		q    float64
		want time.Duration
	}{{0.01, 1}, {0.5, 50}, {0.9, 90}, {0.95, 95}, {0.99, 99}, {1, 100}} {
		if got := h.Quantile(tc.q); got != tc.want {
			t.Errorf("Quantile(%v) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestHistogramNegativeIsZero(t *testing.T) {
	var h Histogram
	h.Record(-time.Second)
	if h.Min() != 0 || h.Max() != 0 || h.Quantile(0.5) != 0 {
		t.Fatalf("negative duration: min=%v max=%v p50=%v, want 0", h.Min(), h.Max(), h.Quantile(0.5))
	}
}

func TestHistogramReset(t *testing.T) {
	var h Histogram
	h.Record(time.Hour)
	h.Reset()
	if h.Count() != 0 || h.Max() != 0 || h.Quantile(0.5) != 0 {
		t.Fatalf("after Reset: count=%d max=%v p50=%v", h.Count(), h.Max(), h.Quantile(0.5))
	}
	h.Record(time.Microsecond)
	if got := h.Quantile(1); got != time.Microsecond {
		t.Fatalf("Quantile(1) after Reset = %v, want 1µs", got)
	}
}

func TestQuantileRelativeError(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	// Log-normal around 200µs, like round trips on a LAN.
	values := make([]time.Duration, 50000)
	for i := range values {
		values[i] = time.Duration(math.Exp(r.NormFloat64()*1.5 + math.Log(200e3)))
	}
	checkQuantiles(t, values)
}

// checkQuantiles checks the quantiles tcpulse reports against the exact
// nearest-rank quantiles of values.
func checkQuantiles(t *testing.T, values []time.Duration) {
	t.Helper()
	var h Histogram
	for _, v := range values {
		h.Record(v)
	}
	sorted := slices.Clone(values)
	for i, v := range sorted {
		sorted[i] = max(v, 0)
	}
	slices.Sort(sorted)
	for _, q := range []float64{0.5, 0.9, 0.95, 0.99, 1} {
		want := sorted[ceilMul(q, int64(len(sorted)))-1]
		got := h.Quantile(q)
		if got < want || float64(got-want) > float64(want)*maxRelativeError {
			t.Fatalf("Quantile(%v) = %v, want %v within +%g", q, got, want, maxRelativeError)
		}
	}
}

func FuzzQuantile(f *testing.F) {
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		// Every 3 bytes make a duration of up to about 16ms, scaled by the
		// next byte to cover larger magnitudes too.
		var values []time.Duration
		for len(data) >= 4 {
			v := time.Duration(data[0]) | time.Duration(data[1])<<8 | time.Duration(data[2])<<16
			values = append(values, v<<(data[3]%40))
			data = data[4:]
		}
		if len(values) == 0 {
			return
		}
		checkQuantiles(t, values)
	})
}

func BenchmarkHistogramRecord(b *testing.B) {
	var h Histogram
	r := rand.New(rand.NewPCG(5, 6))
	values := make([]time.Duration, 1024)
	for i := range values {
		values[i] = time.Duration(r.Int64N(int64(10 * time.Millisecond)))
	}
	i := 0
	for b.Loop() {
		h.Record(values[i%len(values)])
		i++
	}
}
