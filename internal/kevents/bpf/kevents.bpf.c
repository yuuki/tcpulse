// SPDX-License-Identifier: (GPL-2.0-only OR BSD-2-Clause)
/* Copyright (c) 2026 Yuuki Tsubouchi */

/*
 * Counts kernel TCP events on the connections of one process (tcpulse
 * --kernel-events). Tracepoints only, through BTF (tp_btf): there is no
 * hook on the path of every packet or message.
 *
 *   inet_sock_set_state   CLOSE -> SYN_SENT runs inside connect(2), in the
 *                         connecting process, even when TCP Fast Open
 *                         defers the SYN to the first write. A socket of
 *                         the target process gets its storage here.
 *   tcp_retransmit_skb    a retransmission, classified (count_retransmit).
 *   tcp_receive_reset     a reset that the connection accepted.
 *   kfree_skb             a packet of the connection that the kernel
 *                         dropped, by reason (count_drop).
 *
 * Only sockets with storage are counted: other processes and passively
 * opened sockets cost one storage lookup.
 *
 * Every count also bumps events_generation, which user space reads from
 * shared memory: while it does not change, no connection's counters can
 * have changed, and reading them (a system call) can be skipped.
 */

#include "kevents.h"
#include "bpf_helpers.h"
#include "bpf_tracing.h"
#include "bpf_core_read.h"

char LICENSE[] SEC("license") = "Dual BSD/GPL";

/* The target process, in the PID namespace whose nsfs device and inode
 * these are (tcpulse may run in a container). Set at load time. */
const volatile __u64 pidns_dev = 0;
const volatile __u64 pidns_ino = 0;
const volatile __u32 target_tgid = 0;

/* Bumped after the counter of a connection, by a fetching atomic add: the
 * value it returns is stored, so that clang emits BPF_FETCH (-mcpu=v3),
 * which JITs to a full barrier (lock xadd on x86, ldaddal on arm64). A
 * reader that sees the new value then also sees the new counter. */
__u64 events_generation = 0;

static __always_inline void bump_generation(void)
{
	volatile __u64 old;

	old = __sync_fetch_and_add(&events_generation, 1);
	(void)old;
}

struct {
	__uint(type, BPF_MAP_TYPE_SK_STORAGE);
	__uint(map_flags, BPF_F_NO_PREALLOC);
	__type(key, int);
	__type(value, struct kevents_counts);
} conns SEC(".maps");

static __always_inline int in_target(void)
{
	struct bpf_pidns_info ns = {};

	if (bpf_get_ns_current_pid_tgid(pidns_dev, pidns_ino, &ns, sizeof(ns)))
		return 0;
	return ns.tgid == target_tgid;
}

SEC("tp_btf/inet_sock_set_state")
int BPF_PROG(kevents_connect, const struct sock *sk, const int oldstate,
	     const int newstate)
{
	if (oldstate != TCP_CLOSE || newstate != TCP_SYN_SENT || !in_target())
		return 0;
	bpf_sk_storage_get(&conns, (struct sock *)sk, 0,
			   BPF_SK_STORAGE_GET_F_CREATE);
	return 0;
}

/*
 * The kind of a retransmission:
 *   - of the SYN: the socket is still in SYN_SENT;
 *   - of the data of a Fast Open SYN that the server did not acknowledge,
 *     which tcp_rcv_fastopen_synack sends again right after the handshake:
 *     the first other retransmission of a connection whose SYN carried data
 *     (syn_data) not acknowledged (!syn_data_acked). That resend also puts
 *     the socket in the loss state, so it is told apart first. When the SYN
 *     itself was retransmitted, the resend is a consequence of that loss
 *     (SYN retries carry no data: tcp_send_syn_data), and it is counted
 *     with the SYN;
 *   - on a timeout: the socket is in the loss state (TCP_CA_Loss), which a
 *     retransmission timeout enters;
 *   - any other: fast retransmit, RACK, tail loss probe.
 */
/* first_retransmit reports whether this is the first retransmission of the
 * connection other than of the SYN, and remembers that it was seen. */
static __always_inline int first_retransmit(struct kevents_counts *c)
{
	if (c->flags & KEVENTS_RETRANSMITTED)
		return 0;
	c->flags |= KEVENTS_RETRANSMITTED;
	return 1;
}

static __always_inline void count_retransmit(const struct sock *sk)
{
	const struct inet_connection_sock *icsk = (const void *)sk;
	const struct tcp_sock *tp = (const void *)sk;
	struct kevents_counts *c;
	__u32 *counter;

	c = bpf_sk_storage_get(&conns, (struct sock *)sk, 0, 0);
	if (!c)
		return;
	if (BPF_CORE_READ(sk, __sk_common.skc_state) == TCP_SYN_SENT)
		counter = &c->syn_retrans;
	else if (first_retransmit(c) &&
		 BPF_CORE_READ_BITFIELD_PROBED(tp, syn_data) &&
		 !BPF_CORE_READ_BITFIELD_PROBED(tp, syn_data_acked))
		counter = c->syn_retrans ? &c->syn_retrans : &c->fastopen_retrans;
	else if (BPF_CORE_READ_BITFIELD_PROBED(icsk, icsk_ca_state) == TCP_CA_Loss)
		counter = &c->rto_retrans;
	else
		counter = &c->other_retrans;
	__sync_fetch_and_add(counter, 1);
	bump_generation();
}

/*
 * tcp_retransmit_skb has two prototypes. Before Linux 6.17 it fires only
 * for a retransmission that was sent (sk, skb). Since then it also fires
 * for a failed attempt, with its error (sk, skb, err). The loader keeps the
 * program that matches the kernel's prototype.
 */
SEC("tp_btf/tcp_retransmit_skb")
int BPF_PROG(kevents_retransmit, const struct sock *sk, const struct sk_buff *skb)
{
	count_retransmit(sk);
	return 0;
}

SEC("tp_btf/tcp_retransmit_skb")
int BPF_PROG(kevents_retransmit_err, const struct sock *sk,
	     const struct sk_buff *skb, int err)
{
	if (err)
		return 0;
	count_retransmit(sk);
	return 0;
}

SEC("tp_btf/tcp_receive_reset")
int BPF_PROG(kevents_reset, struct sock *sk)
{
	struct kevents_counts *c;

	c = bpf_sk_storage_get(&conns, sk, 0, 0);
	if (c) {
		__sync_fetch_and_add(&c->resets_received, 1);
		bump_generation();
	}
	return 0;
}

/*
 * skb_counts returns the counts of the connection that skb belongs to by
 * skb->sk, or NULL. A packet whose sk is set without a destructor is a
 * fragment queued for reassembly (struct sk_buff in kevents.h): its sk is an
 * offset.
 */
static __always_inline struct kevents_counts *skb_counts(const struct sk_buff *skb)
{
	if (!skb->destructor)
		return 0;
	return bpf_sk_storage_get(&conns, skb->sk, 0, 0);
}

/*
 * count_drop counts a dropped packet under its reason: in the slot that has
 * the reason, or else in the first free slot, which an atomic
 * compare-and-exchange takes (two CPUs may drop packets of the connection at
 * once), or else in other_drops. A slot holds the reason plus one, so that 0
 * marks it free.
 */
static __always_inline void count_drop(struct kevents_counts *c, __u32 reason)
{
	__u32 *counter = &c->other_drops;
	int i;

	reason++;
	/* A reason of 0xffffffff, which no kernel has, cannot be kept. */
	if (reason == 0)
		goto count;
	for (i = 0; i < KEVENTS_DROP_SLOTS; i++) {
		struct kevents_drop_count *d = &c->drops[i];
		__u32 r = *(volatile __u32 *)&d->reason;

		if (r == 0) {
			r = __sync_val_compare_and_swap(&d->reason, 0, reason);
			if (r == 0)
				r = reason;
		}
		if (r == reason) {
			counter = &d->count;
			break;
		}
	}
count:
	__sync_fetch_and_add(counter, 1);
	bump_generation();
}

/*
 * kfree_skb fires for every packet that the kernel drops, with the reason
 * (Linux 5.17), and not for those it consumes (consume_skb). A dropped
 * packet belongs to a connection
 *   - that sent it: skb->sk, until the packet leaves the host (or loops back
 *     to it: loopback_xmit orphans it);
 *   - that it was received for: rx_sk, which the TCP receive path passes
 *     once it found the socket (Linux 6.11), or skb->sk, which early demux
 *     sets to the socket of an established connection before routing, e.g.
 *     for a drop in netfilter's INPUT hook.
 * The loader keeps the program that matches the kernel's prototype.
 */
SEC("tp_btf/kfree_skb")
int BPF_PROG(kevents_drop, struct sk_buff *skb, void *location, int reason)
{
	struct kevents_counts *c = skb_counts(skb);

	if (c)
		count_drop(c, reason);
	return 0;
}

/*
 * rx_sk is NULL for most drops, but some verifiers (Linux 6.11 and 6.12)
 * take the arguments of tracepoints for pointers that are never NULL, and
 * would remove a test of it with its other branch. The helper takes NULL,
 * and its result may be NULL: two lookups cannot be pruned.
 */
SEC("tp_btf/kfree_skb")
int BPF_PROG(kevents_drop_rx, struct sk_buff *skb, void *location, int reason,
	     struct sock *rx_sk)
{
	struct kevents_counts *c = bpf_sk_storage_get(&conns, rx_sk, 0, 0);

	if (!c)
		c = skb_counts(skb);
	if (c)
		count_drop(c, reason);
	return 0;
}
