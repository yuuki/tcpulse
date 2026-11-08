package stats

import (
	"maps"
	"time"
)

// DropSlots is the number of reasons for which Events counts the dropped
// packets of a connection apart.
const DropSlots = 4

// DropCount counts the dropped packets of a connection for one reason.
type DropCount struct {
	// Reason is the kernel's reason (enum skb_drop_reason). 0 marks a slot
	// without a reason yet: the kernel never drops a packet with it.
	Reason uint32
	Count  uint32
}

// Events counts the kernel events of a TCP connection (--kernel-events):
// its retransmissions, by kind, the resets it received, and its packets
// that the kernel dropped, by reason.
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
	// Drops counts the dropped packets of the connection for the first
	// DropSlots reasons met, in the order they were met. A slot keeps its
	// reason once it has one.
	Drops [DropSlots]DropCount
	// OtherDrops counts the dropped packets of the reasons met after the
	// slots were all taken.
	OtherDrops uint32
}

// Sub returns the events counted since prev, an earlier reading of the same
// connection. Its slots keep only the reasons of the drops counted since.
func (e Events) Sub(prev Events) Events {
	d := Events{
		SYNRetrans:      e.SYNRetrans - prev.SYNRetrans,
		FastOpenRetrans: e.FastOpenRetrans - prev.FastOpenRetrans,
		RTORetrans:      e.RTORetrans - prev.RTORetrans,
		OtherRetrans:    e.OtherRetrans - prev.OtherRetrans,
		ResetsReceived:  e.ResetsReceived - prev.ResetsReceived,
		OtherDrops:      e.OtherDrops - prev.OtherDrops,
	}
	for i, slot := range e.Drops {
		if n := slot.Count - prev.Drops[i].Count; n > 0 {
			d.Drops[i] = DropCount{Reason: slot.Reason, Count: n}
		}
	}
	return d
}

// DroppedPackets returns the number of dropped packets, whatever their
// reason.
func (e Events) DroppedPackets() uint32 {
	n := e.OtherDrops
	for _, slot := range e.Drops {
		n += slot.Count
	}
	return n
}

// IsZero reports whether no event was counted. A slot that has a reason but
// no drop yet (the kernel takes the slot, then counts) is not an event.
func (e Events) IsZero() bool {
	return e.SYNRetrans == 0 && e.FastOpenRetrans == 0 && e.RTORetrans == 0 &&
		e.OtherRetrans == 0 && e.ResetsReceived == 0 && e.DroppedPackets() == 0
}

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
	// DropRoundTrips is the number of completed round trips during which at
	// least one packet of their connection was dropped.
	DropRoundTrips int64
	// Drops counts the dropped packets of the connections by reason (the
	// kernel's enum skb_drop_reason), during round trips or not. It is nil
	// when none was dropped.
	Drops map[uint32]int64
	// OtherDrops counts the dropped packets whose reason was not kept: those
	// of a connection that met more than DropSlots reasons.
	OtherDrops int64
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

// addTotals adds the events that the summary counts whether a completed
// round trip owns them or not: resets and drops.
func (s *eventStats) addTotals(ev Events) {
	s.summary.ResetsReceived += int64(ev.ResetsReceived)
	s.summary.OtherDrops += int64(ev.OtherDrops)
	for _, slot := range ev.Drops {
		if slot.Count == 0 {
			continue
		}
		if s.summary.Drops == nil {
			s.summary.Drops = make(map[uint32]int64)
		}
		s.summary.Drops[slot.Reason] += int64(slot.Count)
	}
}

func (s *eventStats) roundTrip(d time.Duration, ev Events) {
	s.addTotals(ev)
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
	count(ev.DroppedPackets(), &s.summary.DropRoundTrips)
}

func (s *eventStats) unmatched(ev Events) {
	s.addTotals(ev)
	s.summary.Unmatched.SYN += int64(ev.SYNRetrans)
	s.summary.Unmatched.FastOpen += int64(ev.FastOpenRetrans)
	s.summary.Unmatched.RTO += int64(ev.RTORetrans)
	s.summary.Unmatched.Other += int64(ev.OtherRetrans)
}

func (s *eventStats) summarize() EventSummary {
	sum := s.summary
	sum.Drops = maps.Clone(s.summary.Drops)
	sum.WithEvents = summarizeLatencies(&s.withEvents)
	sum.WithoutEvents = summarizeLatencies(&s.withoutEvents)
	return sum
}
