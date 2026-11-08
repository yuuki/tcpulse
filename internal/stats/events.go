package stats

import "time"

// Events counts the kernel events of a TCP connection (--kernel-events):
// its retransmissions, by kind, and the resets it received.
type Events struct {
	// SYNRetrans counts the retransmissions of the SYN.
	SYNRetrans uint32
	// FastOpenRetrans counts the data of a TCP Fast Open SYN sent again
	// after the handshake, because the server did not acknowledge it.
	FastOpenRetrans uint32
	// RTORetrans counts the retransmissions in the loss state, which a
	// retransmission timeout enters.
	RTORetrans uint32
	// OtherRetrans counts the other retransmissions: fast retransmit, RACK,
	// tail loss probes.
	OtherRetrans uint32
	// ResetsReceived counts the resets the connection accepted.
	ResetsReceived uint32
}

// Sub returns the events counted since prev, an earlier reading of the same
// connection.
func (e Events) Sub(prev Events) Events {
	return Events{
		SYNRetrans:      e.SYNRetrans - prev.SYNRetrans,
		FastOpenRetrans: e.FastOpenRetrans - prev.FastOpenRetrans,
		RTORetrans:      e.RTORetrans - prev.RTORetrans,
		OtherRetrans:    e.OtherRetrans - prev.OtherRetrans,
		ResetsReceived:  e.ResetsReceived - prev.ResetsReceived,
	}
}

// IsZero reports whether no event was counted.
func (e Events) IsZero() bool { return e == Events{} }

// RetransCounts counts retransmissions, or round trips with retransmissions,
// by kind.
type RetransCounts struct {
	SYN      int64
	FastOpen int64
	RTO      int64
	Other    int64
}

// Total returns the sum of the counts.
func (c RetransCounts) Total() int64 { return c.SYN + c.FastOpen + c.RTO + c.Other }

// LatencySummary describes some of the round trips of a run.
type LatencySummary struct {
	Count int64
	P99   time.Duration
	Max   time.Duration
}

func summarizeLatencies(h *Histogram) LatencySummary {
	return LatencySummary{Count: h.Count(), P99: h.Quantile(0.99), Max: h.Max()}
}

// EventSummary describes the kernel events of the round trips of a run.
type EventSummary struct {
	// RoundTrips is the number of completed round trips during which at
	// least one event was counted on their connection.
	RoundTrips int64
	// RetransRoundTrips counts, by kind, the round trips during which at
	// least one retransmission of that kind was counted.
	RetransRoundTrips RetransCounts
	// WithEvents and WithoutEvents describe the latencies of the round trips
	// with events, and of the others.
	WithEvents    LatencySummary
	WithoutEvents LatencySummary
	// ResetsReceived is the number of resets received by the connections,
	// during round trips or not.
	ResetsReceived int64
	// Unmatched counts the retransmissions that no completed round trip
	// owns: those of requests that failed or were cut off by the end of the
	// run, and those of a handshake outside the clock.
	Unmatched RetransCounts
	// Untracked is the number of completed round trips whose events could
	// not be read.
	Untracked int64
}

// eventStats accumulates an EventSummary; Recorder guards it.
type eventStats struct {
	summary       EventSummary
	withEvents    Histogram
	withoutEvents Histogram
}

func (s *eventStats) addResets(ev Events) {
	s.summary.ResetsReceived += int64(ev.ResetsReceived)
}

func (s *eventStats) roundTrip(d time.Duration, ev Events) {
	s.addResets(ev)
	if ev.IsZero() {
		s.withoutEvents.Record(d)
		return
	}
	s.withEvents.Record(d)
	s.summary.RoundTrips++
	count := func(n uint32, total *int64) {
		if n > 0 {
			*total++
		}
	}
	count(ev.SYNRetrans, &s.summary.RetransRoundTrips.SYN)
	count(ev.FastOpenRetrans, &s.summary.RetransRoundTrips.FastOpen)
	count(ev.RTORetrans, &s.summary.RetransRoundTrips.RTO)
	count(ev.OtherRetrans, &s.summary.RetransRoundTrips.Other)
}

func (s *eventStats) unmatched(ev Events) {
	s.addResets(ev)
	s.summary.Unmatched.SYN += int64(ev.SYNRetrans)
	s.summary.Unmatched.FastOpen += int64(ev.FastOpenRetrans)
	s.summary.Unmatched.RTO += int64(ev.RTORetrans)
	s.summary.Unmatched.Other += int64(ev.OtherRetrans)
}

func (s *eventStats) summarize() EventSummary {
	sum := s.summary
	sum.WithEvents = summarizeLatencies(&s.withEvents)
	sum.WithoutEvents = summarizeLatencies(&s.withoutEvents)
	return sum
}
