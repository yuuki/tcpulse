//go:build linux && (386 || amd64 || arm || arm64 || loong64 || mips64le || mipsle || ppc64le || riscv64)

package kevents

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/link"
	"golang.org/x/sys/unix"

	"github.com/yuuki/tcpulse/internal/stats"
)

// Supported reports whether Start can work on this platform: Linux, with the
// little-endian architectures the object is built for.
const Supported = true

// The slots of the programs (KEVENTS_DROP_SLOTS) are those of stats.Events.
var _ [stats.DropSlots]keventsKeventsDropCount = keventsKeventsCounts{}.Drops

// Tracer counts kernel TCP events on the connections that this process opens
// while it runs, and how long its threads wait for a CPU. ReadEvents and
// Generation may be called concurrently, but not concurrently with Close.
type Tracer struct {
	coll  *ebpf.Collection
	conns *ebpf.Map
	// runq holds the waits for a CPU, or is nil when they are not measured.
	runq  *ebpf.Map
	links []link.Link
	// generation points to events_generation in genMem, the memory the
	// programs share with the process; it is nil, and genErr says why, when
	// that memory is not mapped.
	generation *uint64
	genMem     []byte
	genErr     error
	// drop is the program that counts drops, or empty when they are not
	// counted, and dropErr says why; dropReasons names the reasons. The
	// program is in a collection of its own, dropColl.
	drop        string
	dropErr     error
	dropReasons map[uint32]string
	dropColl    *ebpf.Collection
}

// Start loads and attaches the programs of what opts asks for. It needs a
// kernel with BTF (Linux 5.12 or later: socket storage in tracing programs,
// fetching atomics), and root or CAP_BPF and CAP_PERFMON.
func Start(opts Options) (*Tracer, error) {
	if !opts.Events && !opts.RunQueue {
		return nil, errors.New("nothing to trace")
	}
	if err := checkPrivileges(); err != nil {
		return nil, err
	}
	spec, err := loadKevents()
	if err != nil {
		return nil, fmt.Errorf("reading the eBPF object: %w", err)
	}
	if err := setTarget(spec); err != nil {
		return nil, err
	}
	kernel, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, fmt.Errorf("reading the kernel's BTF (CONFIG_DEBUG_INFO_BTF): %w", err)
	}
	t := &Tracer{}
	// The programs to attach: the variants that match the kernel's
	// tracepoints, as the others would not load. The drop program loads
	// apart (loadDrops).
	dropSpec := spec.Copy()
	var progs []string
	if opts.Events {
		retransmit, err := retransmitProgram(kernel)
		if err != nil {
			return nil, err
		}
		progs = append(progs, keventsProgKeventsConnect, retransmit, keventsProgKeventsReset)
		t.drop, t.dropReasons, t.dropErr = dropProgram(kernel)
	} else {
		t.dropErr = errors.New("kernel events are not counted")
	}
	if opts.RunQueue {
		sw, err := switchProgram(kernel)
		if err != nil {
			return nil, err
		}
		progs = append(progs, keventsProgKeventsWakeup, keventsProgKeventsWakeupNew, sw)
	}
	for name := range spec.Programs {
		if !slices.Contains(progs, name) {
			delete(spec.Programs, name)
		}
	}

	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, loadError(err)
	}
	t.coll, t.conns = coll, coll.Maps[keventsMapConns]
	if opts.Events {
		// Without the mapping, every reading is a system call.
		t.generation, t.genMem, t.genErr = mapGeneration(spec, coll)
	} else {
		t.genErr = errors.New("kernel events are not counted")
	}
	if opts.RunQueue {
		t.runq = coll.Maps[keventsMapRunq]
	}
	for _, name := range progs {
		if err := t.attach(coll.Programs[name], name); err != nil {
			_ = t.Close()
			return nil, loadError(err)
		}
	}
	if t.drop != "" {
		if err := t.loadDrops(dropSpec); err != nil {
			// The drops are an addition to the other events: count those
			// without them, and say why.
			t.drop, t.dropReasons, t.dropErr = "", nil, err
		}
	}
	return t, nil
}

// attach attaches the program prog, called name.
func (t *Tracer) attach(prog *ebpf.Program, name string) error {
	l, err := link.AttachTracing(link.TracingOptions{Program: prog})
	if err != nil {
		return fmt.Errorf("attaching %s: %w", name, err)
	}
	t.links = append(t.links, l)
	return nil
}

// loadDrops loads and attaches the drop program of spec, in a collection of
// its own that shares the maps the other programs loaded: a failure of the
// drop program is told apart from the others', and only stops the drops from
// being counted.
func (t *Tracer) loadDrops(spec *ebpf.CollectionSpec) error {
	for name := range spec.Programs {
		if name != t.drop {
			delete(spec.Programs, name)
		}
	}
	coll, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{
		MapReplacements: map[string]*ebpf.Map{
			keventsMapConns: t.conns,
			".bss":          t.coll.Maps[".bss"],
		},
	})
	if err != nil {
		return fmt.Errorf("loading %s: %w", t.drop, err)
	}
	t.dropColl = coll
	return t.attach(coll.Programs[t.drop], t.drop)
}

// checkPrivileges checks the capabilities the programs need: loading BTF and
// socket storage needs CAP_BPF, tracing programs CAP_PERFMON (CAP_SYS_ADMIN
// grants both). Without them, the feature probes of the library fail too,
// and it reports the features as missing (the map of the programs as
// needing Linux 4.6) rather than the permission as denied; and unprivileged
// users may still create simple maps (kernel.unprivileged_bpf_disabled=0),
// so creating one would not tell.
func checkPrivileges() error {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("reading the capabilities of the process: %w", err)
	}
	has := func(c int) bool { return data[c/32].Effective&(1<<(c%32)) != 0 }
	if !has(unix.CAP_SYS_ADMIN) && (!has(unix.CAP_BPF) || !has(unix.CAP_PERFMON)) {
		return fmt.Errorf("loading the eBPF programs needs root, or CAP_BPF and CAP_PERFMON: %w", os.ErrPermission)
	}
	return nil
}

// setTarget makes the programs count the sockets of this process, as its
// PID namespace sees it.
func setTarget(spec *ebpf.CollectionSpec) error {
	var st unix.Stat_t
	if err := unix.Stat("/proc/self/ns/pid", &st); err != nil {
		return fmt.Errorf("reading the PID namespace: %w", err)
	}
	// The kernel's PIDs are 32-bit (pid_t), and at most 2^22 (pid_max).
	pid := os.Getpid()
	if pid < 0 || uint64(pid) > math.MaxUint32 {
		return fmt.Errorf("unexpected process ID %d", pid)
	}
	for name, value := range map[string]any{
		// Stat_t.Dev is 32-bit on some architectures (mipsle, mips64le):
		// the variables are __u64.
		keventsVarPidnsDev:   uint64(st.Dev), //nolint:unconvert // see above
		keventsVarPidnsIno:   uint64(st.Ino), //nolint:unconvert // see above
		keventsVarTargetTgid: uint32(pid),
	} {
		v, ok := spec.Variables[name]
		if !ok {
			return fmt.Errorf("the eBPF object has no variable %s", name)
		}
		if err := v.Set(value); err != nil {
			return fmt.Errorf("setting %s: %w", name, err)
		}
	}
	return nil
}

// tracepointParams returns the parameters of a tracepoint, as the kernel's
// BTF declares them.
func tracepointParams(kernel *btf.Spec, name string) ([]btf.FuncParam, error) {
	var typedef *btf.Typedef
	if err := kernel.TypeByName("btf_trace_"+name, &typedef); err != nil {
		return nil, fmt.Errorf("finding the %s tracepoint: %w", name, err)
	}
	ptr, ok := typedef.Type.(*btf.Pointer)
	if !ok {
		return nil, fmt.Errorf("unexpected type of the %s tracepoint: %v", name, typedef.Type)
	}
	proto, ok := ptr.Target.(*btf.FuncProto)
	if !ok || len(proto.Params) == 0 {
		return nil, fmt.Errorf("unexpected type of the %s tracepoint: %v", name, ptr.Target)
	}
	// The first parameter is the tracepoint's context.
	return proto.Params[1:], nil
}

// retransmitProgram returns the retransmission program that matches the
// kernel's tcp_retransmit_skb tracepoint: Linux 6.17 added an err argument
// (and fires it for failed attempts too).
func retransmitProgram(kernel *btf.Spec) (string, error) {
	params, err := tracepointParams(kernel, "tcp_retransmit_skb")
	if err != nil {
		return "", err
	}
	switch len(params) {
	case 2:
		return keventsProgKeventsRetransmit, nil
	case 3:
		return keventsProgKeventsRetransmitErr, nil
	default:
		return "", fmt.Errorf("unexpected prototype of the tcp_retransmit_skb tracepoint: %d parameters", len(params))
	}
}

// switchProgram returns the context switch program that matches the
// kernel's sched_switch tracepoint: Linux 5.18 added prev_state.
func switchProgram(kernel *btf.Spec) (string, error) {
	params, err := tracepointParams(kernel, "sched_switch")
	if err != nil {
		return "", err
	}
	switch len(params) {
	case 3:
		return keventsProgKeventsSwitch, nil
	case 4:
		return keventsProgKeventsSwitchState, nil
	default:
		return "", fmt.Errorf("unexpected prototype of the sched_switch tracepoint: %d parameters", len(params))
	}
}

// dropReasonPrefix starts the names of the values of enum skb_drop_reason.
const dropReasonPrefix = "SKB_DROP_REASON_"

// dropProgram returns the drop program that matches the kernel's kfree_skb
// tracepoint, and the names of the drop reasons, or why drops cannot be
// counted. Linux 5.17 added the reason (skb, location, reason), and Linux
// 6.11 the socket a packet was received for (rx_sk).
func dropProgram(kernel *btf.Spec) (string, map[uint32]string, error) {
	params, err := tracepointParams(kernel, "kfree_skb")
	if err != nil {
		return "", nil, err
	}
	if len(params) < 3 {
		return "", nil, errors.New("the kernel's kfree_skb tracepoint has no reason (Linux 5.17 added it)")
	}
	reasons, ok := btf.UnderlyingType(params[2].Type).(*btf.Enum)
	if !ok || reasons.Name != "skb_drop_reason" {
		return "", nil, fmt.Errorf("unexpected reason of the kfree_skb tracepoint: %v", params[2].Type)
	}
	var prog string
	switch len(params) {
	case 3:
		prog = keventsProgKeventsDrop
	case 4:
		prog = keventsProgKeventsDropRx
	default:
		return "", nil, fmt.Errorf("unexpected prototype of the kfree_skb tracepoint: %d parameters", len(params))
	}
	names := make(map[uint32]string, len(reasons.Values))
	for _, v := range reasons.Values {
		// SKB_DROP_REASON_MAX and SUBSYS_MASK are bounds, not reasons; the
		// reasons of a subsystem (the high 16 bits) have no name here.
		name, ok := strings.CutPrefix(v.Name, dropReasonPrefix)
		if !ok || name == "MAX" || name == "SUBSYS_MASK" || v.Value > math.MaxUint32 {
			continue
		}
		names[uint32(v.Value)] = name
	}
	return prog, names, nil
}

// mapGeneration maps, read-only, the memory of the programs' zeroed global
// variables (.bss), and returns a pointer to events_generation in it, and
// the mapping. The library maps it too, but gives typed pointers into it
// only behind an experimental build tag, and its ReadAt is a plain copy:
// Generation needs an atomic load, with acquire ordering.
func mapGeneration(spec *ebpf.CollectionSpec, coll *ebpf.Collection) (*uint64, []byte, error) {
	// The programs bump the generation with a fetching atomic add. The JITs
	// make it a full barrier on amd64 and arm64; on other architectures,
	// older kernels do not (ppc64 and riscv64 before Linux 6.10), and a
	// reader could see the generation before the counter it follows.
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		return nil, nil, fmt.Errorf("not on %s, whose kernels do not all order the generation after the counters", runtime.GOARCH)
	}
	const section = ".bss"
	ms, ok := spec.Maps[section]
	if !ok {
		return nil, nil, fmt.Errorf("no %s in the eBPF object", section)
	}
	ds, ok := ms.Value.(*btf.Datasec)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected type of %s: %v", section, ms.Value)
	}
	off := -1
	for _, v := range ds.Vars {
		if v.Type.TypeName() == keventsVarEventsGeneration {
			off = int(v.Offset)
		}
	}
	m := coll.Maps[section]
	if off < 0 || off%8 != 0 || m == nil {
		return nil, nil, fmt.Errorf("no aligned %s in %s", keventsVarEventsGeneration, section)
	}
	if m.Flags()&unix.BPF_F_MMAPABLE == 0 {
		return nil, nil, fmt.Errorf("%s is not mmapable", section)
	}
	size := int(m.ValueSize() * m.MaxEntries())
	if off+8 > size {
		return nil, nil, fmt.Errorf("%s out of %s", keventsVarEventsGeneration, section)
	}
	page := os.Getpagesize()
	mem, err := unix.Mmap(m.FD(), 0, (size+page-1)/page*page, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("mapping %s: %w", section, err)
	}
	// The mapping is outside the Go heap, page-aligned, and off is aligned
	// to 8 bytes: an atomic 64-bit load from it is valid.
	return (*uint64)(unsafe.Pointer(&mem[off])), mem, nil
}

// loadError adds what is likely missing to an error loading the programs.
func loadError(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("loading the eBPF programs (needs root, or CAP_BPF and CAP_PERFMON): %w", err)
	}
	return fmt.Errorf("loading the eBPF programs (needs Linux 5.12 or later with BTF): %w", err)
}

// Generation returns a number that changes whenever an event is counted on
// any connection, read from shared memory without a system call, and false
// when it is not available (see GenerationError). While it does not change,
// ReadEvents returns what it returned before for every socket. The programs
// change it after the counter of a socket, with a full barrier, so a
// Generation read before ReadEvents never hides an event from the next
// comparison.
func (t *Tracer) Generation() (uint64, bool) {
	if t.generation == nil {
		return 0, false
	}
	return atomic.LoadUint64(t.generation), true
}

// GenerationError says why Generation is not available, or is nil.
func (t *Tracer) GenerationError() error { return t.genErr }

// DropsError says why the drops of packets are not counted (ReadEvents
// returns none), or is nil.
func (t *Tracer) DropsError() error { return t.dropErr }

// DropReasons returns the names of the reasons for which the kernel drops
// packets, without their SKB_DROP_REASON_ prefix, by value. It is nil when
// drops are not counted.
func (t *Tracer) DropReasons() map[uint32]string { return t.dropReasons }

// ReadEvents returns the events counted on the socket whose descriptor is
// fd since it was opened, or ErrNotTraced. Any descriptor of the socket
// works, a duplicate included: it keeps the socket, and its counters, after
// the connection's own descriptor is closed.
func (t *Tracer) ReadEvents(fd int) (stats.Events, error) {
	// The key of socket storage is the socket's file descriptor, a C int.
	if fd < 0 || fd > math.MaxInt32 {
		return stats.Events{}, fmt.Errorf("file descriptor %d out of range", fd)
	}
	var c keventsKeventsCounts
	err := t.conns.Lookup(int32(fd), &c)
	if errors.Is(err, ebpf.ErrKeyNotExist) {
		return stats.Events{}, ErrNotTraced
	}
	if err != nil {
		return stats.Events{}, fmt.Errorf("reading the events of socket %d: %w", fd, err)
	}
	ev := stats.Events{
		SYNRetrans:      c.SynRetrans,
		FastOpenRetrans: c.FastopenRetrans,
		RTORetrans:      c.RtoRetrans,
		OtherRetrans:    c.OtherRetrans,
		ResetsReceived:  c.ResetsReceived,
		OtherDrops:      c.OtherDrops,
	}
	for i, d := range c.Drops {
		// The programs keep the reason plus one, 0 in a free slot. A slot
		// whose count is read without its reason (the copy raced with the
		// program taking the slot) is read again whole at the next reading:
		// the program changes the generation after the count.
		if d.Reason != 0 {
			ev.Drops[i] = stats.DropCount{Reason: d.Reason - 1, Count: d.Count}
		}
	}
	return ev, nil
}

// The buckets of the waits for a CPU (kevents.h).
const (
	runqSubBits = 3
	runqSub     = 1 << runqSubBits
)

// The buckets of the programs are those runqBucketMax decodes.
var _ [(63 - runqSubBits + 2) * runqSub]uint64 = keventsKeventsRunq{}.Buckets

// runqBucketMax returns the largest wait, in nanoseconds, of bucket b
// (runq_bucket in kevents.bpf.c).
func runqBucketMax(b int) uint64 {
	if b < 0 {
		return 0
	}
	if b < runqSub {
		return uint64(b)
	}
	e := b>>runqSubBits + runqSubBits - 1
	sub := uint64(b & (runqSub - 1))
	return (runqSub+sub+1)<<(e-runqSubBits) - 1
}

// RunQueue returns how long the threads of the process waited for a CPU
// since Start, or an error when Start was not asked for it.
func (t *Tracer) RunQueue() (stats.RunQueueSummary, error) {
	if t.runq == nil {
		return stats.RunQueueSummary{}, errors.New("the run queue latency is not measured")
	}
	var perCPU []keventsKeventsRunq
	if err := t.runq.Lookup(uint32(0), &perCPU); err != nil {
		return stats.RunQueueSummary{}, fmt.Errorf("reading the run queue latency: %w", err)
	}
	var sum keventsKeventsRunq
	for _, c := range perCPU {
		sum.Waits += c.Waits
		sum.TotalNs += c.TotalNs
		sum.MaxNs = max(sum.MaxNs, c.MaxNs)
		sum.Preempted += c.Preempted
		for i, n := range c.Buckets {
			sum.Buckets[i] += n
		}
	}
	return summarizeRunQueue(&sum), nil
}

// summarizeRunQueue computes the summary of the waits of r; its percentiles
// are nearest-rank, as the largest wait of their bucket, up to the longest
// wait.
func summarizeRunQueue(r *keventsKeventsRunq) stats.RunQueueSummary {
	ns := func(v uint64) time.Duration { return time.Duration(min(v, math.MaxInt64)) }
	s := stats.RunQueueSummary{
		Waits:     int64(min(r.Waits, math.MaxInt64)),
		Preempted: int64(min(r.Preempted, math.MaxInt64)),
		Total:     ns(r.TotalNs),
		Max:       ns(r.MaxNs),
	}
	// The waits of the buckets: a wait may end between the reading of
	// waits and of its bucket.
	var count uint64
	for _, n := range r.Buckets {
		count += n
	}
	quantile := func(q float64) time.Duration {
		if count == 0 {
			return 0
		}
		rank := uint64(math.Ceil(q * float64(count)))
		var seen uint64
		for b, n := range r.Buckets {
			seen += n
			if seen >= rank {
				return ns(min(runqBucketMax(b), r.MaxNs))
			}
		}
		return s.Max
	}
	s.P50, s.P99 = quantile(0.50), quantile(0.99)
	return s
}

// Close detaches and unloads the programs.
func (t *Tracer) Close() error {
	var errs []error
	for _, l := range t.links {
		errs = append(errs, l.Close())
	}
	t.links = nil
	if t.genMem != nil {
		t.generation = nil
		errs = append(errs, unix.Munmap(t.genMem))
		t.genMem = nil
	}
	if t.dropColl != nil {
		t.dropColl.Close()
		t.dropColl = nil
	}
	if t.coll != nil {
		t.coll.Close()
		t.coll = nil
	}
	return errors.Join(errs...)
}
