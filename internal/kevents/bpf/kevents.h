/* SPDX-License-Identifier: (GPL-2.0-only OR BSD-2-Clause) */
/* Copyright (c) 2026 Yuuki Tsubouchi */

/*
 * The kernel types and constants the programs use, instead of a vmlinux.h.
 *
 * The structs declare only the fields the programs read. They are marked
 * preserve_access_index, so every access is a CO-RE relocation that the
 * loader resolves against the running kernel's BTF: their layout here does
 * not matter, only the struct and field names do. The constants are
 * user-space ABI (linux/bpf.h) or long-stable kernel enums (TCP states,
 * congestion avoidance states).
 */

#ifndef __KEVENTS_H
#define __KEVENTS_H

typedef unsigned char __u8;
typedef short __s16;
typedef unsigned short __u16;
typedef int __s32;
typedef unsigned int __u32;
typedef long long __s64;
typedef unsigned long long __u64;
typedef __u16 __be16;
typedef __u32 __be32;
typedef __u32 __wsum;

/* linux/bpf.h */
enum {
	BPF_MAP_TYPE_SK_STORAGE = 24,
};
#define BPF_F_NO_PREALLOC (1U << 0)
#define BPF_SK_STORAGE_GET_F_CREATE (1ULL << 0)

struct bpf_pidns_info {
	__u32 pid;
	__u32 tgid;
};

/* include/net/tcp_states.h */
#define TCP_SYN_SENT 2
#define TCP_CLOSE 7

/* include/uapi/linux/tcp.h: enum tcp_ca_state */
#define TCP_CA_Loss 4

struct sock_common {
	volatile unsigned char skc_state;
} __attribute__((preserve_access_index));

struct sock {
	struct sock_common __sk_common;
} __attribute__((preserve_access_index));

/*
 * Before Linux 6.9, sk shares a union with ip_defrag_offset, which a
 * fragment queued for reassembly sets: its sk is then not a socket. Such a
 * fragment has no destructor, while every packet that belongs to a socket
 * has one (count_drop). CO-RE finds the fields by name, in a union or not.
 */
struct sk_buff {
	struct sock *sk;
	void (*destructor)(struct sk_buff *skb);
} __attribute__((preserve_access_index));

/* Bit fields: read with BPF_CORE_READ_BITFIELD_PROBED, which relocates
 * their offset, size and shifts. */
struct inet_connection_sock {
	__u8 icsk_ca_state : 5;
} __attribute__((preserve_access_index));

struct tcp_sock {
	__u8 syn_data : 1;
	__u8 syn_data_acked : 1;
} __attribute__((preserve_access_index));

/* The drop reasons a connection keeps apart (count_drop). */
#define KEVENTS_DROP_SLOTS 4

/* The drops of one reason. */
struct kevents_drop_count {
	/* The reason (enum skb_drop_reason) plus one: 0 marks a free slot. On
	 * Linux 5.17, reason 0 is a reason (SKB_DROP_REASON_NOT_SPECIFIED). */
	__u32 reason;
	__u32 count;
};

/*
 * The events counted on a connection, kept in its socket storage. User space
 * reads it by socket file descriptor (internal/kevents: ReadEvents).
 */
struct kevents_counts {
	__u32 syn_retrans;
	__u32 fastopen_retrans;
	__u32 rto_retrans;
	__u32 other_retrans;
	__u32 resets_received;
	__u32 flags;
	/* The drops of the first reasons met, in the order they were met; a
	 * slot keeps its reason once it has one. */
	struct kevents_drop_count drops[KEVENTS_DROP_SLOTS];
	/* The drops of the reasons met after the slots were all taken. */
	__u32 other_drops;
};

/* flags */
#define KEVENTS_RETRANSMITTED (1U << 0) /* a retransmission other than of the SYN was seen */

#endif /* __KEVENTS_H */
