// Package stats records round-trip latencies and summarizes them as the
// count, extremes, mean, percentiles and rate that tcpulse reports.
package stats

import (
	"math/bits"
	"time"
)

// Every power of two of nanoseconds is split into subBucketCount buckets, so
// a recorded duration is known to within 1/subBucketCount (0.8%) of itself,
// and exactly below subBucketCount nanoseconds.
const (
	subBucketBits  = 7
	subBucketCount = 1 << subBucketBits
)

// Histogram counts durations in log-linear buckets: its memory is bounded
// (at most 57×128 counters, allocated as larger values arrive) however many
// durations it records, and its percentiles have a bounded relative error.
// The count, minimum, maximum and mean are exact.
//
// A Histogram is not safe for concurrent use; Recorder serializes access.
type Histogram struct {
	counts []uint64
	count  int64
	sum    time.Duration
	min    time.Duration
	max    time.Duration
}

// Record adds d. A negative duration is recorded as zero.
func (h *Histogram) Record(d time.Duration) {
	d = max(d, 0)
	i := bucketIndex(uint64(d))
	if i >= len(h.counts) {
		h.counts = append(h.counts, make([]uint64, i+1-len(h.counts))...)
	}
	h.counts[i]++
	if h.count == 0 || d < h.min {
		h.min = d
	}
	h.max = max(h.max, d)
	h.count++
	h.sum += d
}

// Reset forgets every recorded duration and keeps the allocated buckets.
func (h *Histogram) Reset() {
	clear(h.counts)
	h.count, h.sum, h.min, h.max = 0, 0, 0, 0
}

// Count returns the number of recorded durations.
func (h *Histogram) Count() int64 { return h.count }

// Min returns the smallest recorded duration, or 0 if there is none.
func (h *Histogram) Min() time.Duration { return h.min }

// Max returns the largest recorded duration, or 0 if there is none.
func (h *Histogram) Max() time.Duration { return h.max }

// Mean returns the arithmetic mean of the recorded durations, or 0 if there
// is none.
func (h *Histogram) Mean() time.Duration {
	if h.count == 0 {
		return 0
	}
	return h.sum / time.Duration(h.count)
}

// Quantile returns the nearest-rank q-quantile (0 < q ≤ 1) of the recorded
// durations, or 0 if there is none: the smallest recorded duration that at
// least q×Count durations do not exceed, rounded up to the end of its bucket
// and clamped to [Min, Max].
func (h *Histogram) Quantile(q float64) time.Duration {
	if h.count == 0 {
		return 0
	}
	rank := uint64(max(1, ceilMul(q, h.count)))
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen >= rank {
			// The highest value of a bucket is at most MaxInt64: the
			// buckets end with that of the largest Duration.
			return min(max(time.Duration(bucketHighest(i)), h.min), h.max) //nolint:gosec // see above
		}
	}
	return h.max
}

// ceilMul returns ⌈q×n⌉ capped to n.
func ceilMul(q float64, n int64) int64 {
	r := q * float64(n)
	k := int64(r)
	if float64(k) < r {
		k++
	}
	return min(k, n)
}

// bucketIndex returns the bucket of v. Values below subBucketCount have a
// bucket each; above, the bucket is given by the position of the highest set
// bit and the subBucketBits bits that follow it.
func bucketIndex(v uint64) int {
	if v < subBucketCount {
		return int(v)
	}
	shift := bits.Len64(v) - subBucketBits - 1
	// v>>shift keeps the subBucketBits+1 highest bits of v: it is below
	// 2×subBucketCount.
	return (shift+1)*subBucketCount + int(v>>uint(shift)) - subBucketCount //nolint:gosec // see above
}

// bucketLowest returns the smallest value of bucket i.
func bucketLowest(i int) uint64 {
	if i < subBucketCount {
		return uint64(i) //nolint:gosec // 0 ≤ i < subBucketCount
	}
	shift := i/subBucketCount - 1
	return uint64(i%subBucketCount+subBucketCount) << uint(shift)
}

// bucketHighest returns the largest value of bucket i.
func bucketHighest(i int) uint64 {
	return bucketLowest(i+1) - 1
}
