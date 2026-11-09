package stats

import "time"

// BreakdownSummary describes how the round trips of a run split into send,
// network and receive time, by the kernel's timestamps of the last byte of
// each message (--latency-breakdown). Every completed round trip counts
// once: with a breakdown, missing a timestamp, with its echo out of order,
// or with a negative part.
type BreakdownSummary struct {
	// RoundTrips is the number of completed round trips with a breakdown.
	RoundTrips int64
	// Missing is the number of completed round trips without one, for
	// lack of a timestamp that it needs.
	Missing int64
	// OutOfOrder is the number of completed round trips whose echo had
	// packets out of order: an earlier one lost or reordered on the way
	// back, whose wait the receive part would take.
	OutOfOrder int64
	// Negative is the number of completed round trips whose breakdown had
	// a negative part, which would come from a step of the system clock or
	// from packets reordered: their breakdown is not used.
	Negative int64
	// Send is from the start of the round trip to the last byte of the
	// message leaving the host; Network, from there to the reception of
	// the last byte of the echo; Receive, from there to the end of the
	// round trip.
	Send    PartSummary
	Network PartSummary
	Receive PartSummary
}

// PartSummary describes one part of the round trips with a breakdown.
type PartSummary struct {
	P50 time.Duration
	P99 time.Duration
}

// breakdownStats accumulates a BreakdownSummary; Recorder guards it.
type breakdownStats struct {
	summary                BreakdownSummary
	send, network, receive Histogram
}

func summarizePart(h *Histogram) PartSummary {
	return PartSummary{P50: h.Quantile(0.50), P99: h.Quantile(0.99)}
}

// RecordBreakdown adds the parts of a completed round trip, none negative.
func (r *Recorder) RecordBreakdown(send, network, receive time.Duration) {
	r.breakdownMu.Lock()
	defer r.breakdownMu.Unlock()
	b := &r.breakdown
	b.summary.RoundTrips++
	b.send.Record(send)
	b.network.Record(network)
	b.receive.Record(receive)
}

// RecordMissingBreakdown counts a completed round trip that lacks a
// timestamp.
func (r *Recorder) RecordMissingBreakdown() {
	r.breakdownMu.Lock()
	defer r.breakdownMu.Unlock()
	r.breakdown.summary.Missing++
}

// RecordOutOfOrderBreakdown counts a completed round trip whose echo had
// packets out of order.
func (r *Recorder) RecordOutOfOrderBreakdown() {
	r.breakdownMu.Lock()
	defer r.breakdownMu.Unlock()
	r.breakdown.summary.OutOfOrder++
}

// RecordNegativeBreakdown counts a completed round trip whose breakdown had
// a negative part.
func (r *Recorder) RecordNegativeBreakdown() {
	r.breakdownMu.Lock()
	defer r.breakdownMu.Unlock()
	r.breakdown.summary.Negative++
}

// Breakdown summarizes the breakdowns recorded during the run.
func (r *Recorder) Breakdown() BreakdownSummary {
	r.breakdownMu.Lock()
	defer r.breakdownMu.Unlock()
	b := &r.breakdown
	s := b.summary
	s.Send, s.Network, s.Receive = summarizePart(&b.send), summarizePart(&b.network), summarizePart(&b.receive)
	return s
}
