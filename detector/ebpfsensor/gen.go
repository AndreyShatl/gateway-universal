package ebpfsensor

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target amd64 -cc clang sensor sensor.c -- -I/usr/include/x86_64-linux-gnu
