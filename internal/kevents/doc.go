// Package kevents counts kernel TCP events on the connections of tcpulse
// with eBPF (--kernel-events): retransmissions, by kind, resets received,
// and dropped packets, by reason. The counts of a connection are read by
// socket, so that the client can attribute them to its round trips.
//
// The programs are in bpf/kevents.bpf.c. The object bpf2go builds from them
// is committed (kevents_bpfel.go, kevents_bpfel.o), so that building tcpulse
// needs no C compiler; `make generate` rebuilds it with clang.
package kevents

//go:generate go tool bpf2go -cc clang -cflags "-O2 -g -Wall -Werror -mcpu=v3" -target bpfel -tags linux -type kevents_counts -type kevents_drop_count kevents bpf/kevents.bpf.c -- -I./bpf/include
