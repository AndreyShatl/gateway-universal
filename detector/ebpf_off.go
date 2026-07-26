//go:build !ebpf

package main

import "log"

// runWatchEBPF — заглушка для обычной сборки (без тега `ebpf`, см. ebpf_on.go).
// eBPF-путь (T59) требует toolchain (clang/libbpf-dev) на этапе go:generate и
// пока только x86_64 — не тянем его в дефолтную сборку install.sh, чтобы не
// ломать установку на других архитектурах/машинах без этого toolchain.
func runWatchEBPF() {
	log.Fatal("watch-ebpf: бинарь собран без поддержки eBPF (нужна пересборка: go build -tags ebpf)")
}
