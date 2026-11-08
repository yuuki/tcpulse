module github.com/yuuki/tcpulse

go 1.27.3

require (
	github.com/cilium/ebpf v0.22.0
	github.com/spf13/pflag v1.0.10
	go.uber.org/ratelimit v0.3.1
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
)

require (
	github.com/benbjohnson/clock v1.3.5 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool github.com/cilium/ebpf/cmd/bpf2go
