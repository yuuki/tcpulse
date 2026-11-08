//go:build linux && (386 || amd64 || arm || arm64 || loong64 || mips64le || mipsle || ppc64le || riscv64)

package kevents

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"sync/atomic"
	"syscall"
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

// Tracer counts kernel TCP events on the connections that this process opens
// while it runs. Its methods may be called concurrently.
type Tracer struct {
	coll  *ebpf.Collection
	conns *ebpf.Map
	links []link.Link
	// generation points to events_generation in genMem, the memory the
	// programs share with the process; it is nil when that memory cannot be
	// mapped.
	generation *uint64
	genMem     []byte
}

// Start loads and attaches the programs. It needs a kernel with BTF (Linux
// 5.12 or later: socket storage in tracing programs, fetching atomics), and
// root or CAP_BPF and CAP_PERFMON.
func Start() (*Tracer, error) {
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
	retransmit, err := retransmitProgram()
	if err != nil {
		return nil, err
	}
	// Keep the variant of the retransmission program that matches the
	// kernel's tracepoint: the other would not load.
	for _, name := range []string{keventsProgKeventsRetransmit, keventsProgKeventsRetransmitErr} {
		if name != retransmit {
			delete(spec.Programs, name)
		}
	}

	coll, err := ebpf.NewCollection(spec)
	if err != nil {
		return nil, loadError(err)
	}
	t := &Tracer{coll: coll, conns: coll.Maps[keventsMapConns]}
	// Without the mapping, every read goes through a system call.
	t.generation, t.genMem, _ = mapGeneration(spec, coll)
	for _, name := range []string{keventsProgKeventsConnect, retransmit, keventsProgKeventsReset} {
		l, err := link.AttachTracing(link.TracingOptions{Program: coll.Programs[name]})
		if err != nil {
			_ = t.Close()
			return nil, loadError(fmt.Errorf("attaching %s: %w", name, err))
		}
		t.links = append(t.links, l)
	}
	return t, nil
}

// checkPrivileges creates the simplest map. Without the privileges, the
// feature probes of the library fail too, and it reports the features as
// missing (the map of the programs as needing Linux 4.6) rather than the
// permission as denied.
func checkPrivileges() error {
	m, err := ebpf.NewMap(&ebpf.MapSpec{Type: ebpf.Array, KeySize: 4, ValueSize: 4, MaxEntries: 1})
	if err != nil {
		return loadError(fmt.Errorf("creating an eBPF map: %w", err))
	}
	return m.Close()
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
		keventsVarPidnsDev:   st.Dev,
		keventsVarPidnsIno:   st.Ino,
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

// retransmitProgram returns the retransmission program that matches the
// kernel's tcp_retransmit_skb tracepoint: Linux 6.17 added an err argument
// (and fires it for failed attempts too).
func retransmitProgram() (string, error) {
	kernel, err := btf.LoadKernelSpec()
	if err != nil {
		return "", fmt.Errorf("reading the kernel's BTF (CONFIG_DEBUG_INFO_BTF): %w", err)
	}
	var typedef *btf.Typedef
	if err := kernel.TypeByName("btf_trace_tcp_retransmit_skb", &typedef); err != nil {
		return "", fmt.Errorf("finding the tcp_retransmit_skb tracepoint: %w", err)
	}
	ptr, ok := typedef.Type.(*btf.Pointer)
	if !ok {
		return "", fmt.Errorf("unexpected type of the tcp_retransmit_skb tracepoint: %v", typedef.Type)
	}
	proto, ok := ptr.Target.(*btf.FuncProto)
	if !ok {
		return "", fmt.Errorf("unexpected type of the tcp_retransmit_skb tracepoint: %v", ptr.Target)
	}
	// The first parameter is the tracepoint's context.
	switch len(proto.Params) {
	case 3:
		return keventsProgKeventsRetransmit, nil
	case 4:
		return keventsProgKeventsRetransmitErr, nil
	default:
		return "", fmt.Errorf("unexpected prototype of the tcp_retransmit_skb tracepoint: %d parameters", len(proto.Params)-1)
	}
}

// mapGeneration maps, read-only, the memory of the programs' zeroed global
// variables (.bss), and returns a pointer to events_generation in it, and
// the mapping. The library maps it too, but gives typed pointers into it
// only behind an experimental build tag, and its ReadAt is a plain copy:
// Generation needs an atomic load, with acquire ordering.
func mapGeneration(spec *ebpf.CollectionSpec, coll *ebpf.Collection) (*uint64, []byte, error) {
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
// when it is not available. While it does not change, ReadEvents returns
// what it returned before for every connection. The programs change it
// after the counter of a connection, with a full barrier, so a Generation
// read before ReadEvents never hides an event from the next comparison.
func (t *Tracer) Generation() (uint64, bool) {
	if t.generation == nil {
		return 0, false
	}
	return atomic.LoadUint64(t.generation), true
}

// ReadEvents returns the events counted on conn since it was opened, or
// ErrNotTraced.
func (t *Tracer) ReadEvents(conn net.Conn) (stats.Events, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return stats.Events{}, fmt.Errorf("not a socket: %T", conn)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return stats.Events{}, fmt.Errorf("getting the socket of %s: %w", conn.RemoteAddr(), err)
	}
	var (
		c         keventsKeventsCounts
		lookupErr error
	)
	if err := raw.Control(func(fd uintptr) {
		// The key of socket storage is the socket's file descriptor, a C
		// int.
		if fd > math.MaxInt32 {
			lookupErr = fmt.Errorf("file descriptor %d out of range", fd)
			return
		}
		lookupErr = t.conns.Lookup(int32(fd), &c)
	}); err != nil {
		return stats.Events{}, fmt.Errorf("reading the events of %s: %w", conn.RemoteAddr(), err)
	}
	if errors.Is(lookupErr, ebpf.ErrKeyNotExist) {
		return stats.Events{}, ErrNotTraced
	}
	if lookupErr != nil {
		return stats.Events{}, fmt.Errorf("reading the events of %s: %w", conn.RemoteAddr(), lookupErr)
	}
	return stats.Events{
		SYNRetrans:      c.SynRetrans,
		FastOpenRetrans: c.FastopenRetrans,
		RTORetrans:      c.RtoRetrans,
		FastRetrans:     c.FastRetrans,
		ResetsReceived:  c.ResetsReceived,
	}, nil
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
	if t.coll != nil {
		t.coll.Close()
		t.coll = nil
	}
	return errors.Join(errs...)
}
