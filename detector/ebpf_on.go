//go:build ebpf

package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"

	"gateway-detector/ebpfsensor"
	"gateway-detector/watcher"
)

// runWatchEBPF — T59: тот же handler (buildCandidateHandler), тот же
// watcher.Watcher (пороги/агрегация не дублируются) — источник событий
// заменён с pcap на eBPF (detector/ebpfsensor). Отдельная команда, не трогает
// боевой `watch` — гонять параллельно, в тени (без --apply), сверяя с pcap.
//
// Собирается ТОЛЬКО с тегом ebpf (`go build -tags ebpf`, требует toolchain
// clang/libbpf-dev на этапе go:generate, x86_64) — без тега этот файл не
// участвует в сборке вообще, см. ebpf_off.go (заглушка для обычной сборки).
func runWatchEBPF() {
	fs := flag.NewFlagSet("watch-ebpf", flag.ExitOnError)
	iface := fs.String("iface", "", "WAN-интерфейс (авто, если пусто)")
	vps := fs.String("vps", "", "IP VPS — исключить туннель (авто из --config-env, если пусто)")
	configEnv := fs.String("config-env", "/root/gateway-universal/config.env", "config.env для VPS_ADDR")
	socks := fs.String("socks", "127.0.0.1:1081", "VPS-socks5 для перепроверки")
	apply := fs.Bool("apply", false, "применять (иначе тень: только лог, что БЫ добавил)")
	fs.Parse(os.Args[2:])
	if *iface == "" {
		*iface = detectIface()
	}
	if *vps == "" {
		*vps = resolveHost(configVar(*configEnv, "VPS_ADDR"))
	}
	if *iface == "" {
		log.Fatal("не удалось определить WAN-интерфейс, задайте --iface")
	}
	gwdbScript = filepath.Join(filepath.Dir(*configEnv), "scripts", "gwdb.py")
	log.Printf("detector(ebpf): iface=%s vps=%s apply=%v", *iface, *vps, *apply)

	sensor, err := ebpfsensor.Load(*iface)
	if err != nil {
		log.Fatalf("watch-ebpf: %v", err)
	}
	defer sensor.Close()

	handler := buildCandidateHandler(apply, socks)
	w := &watcher.Watcher{Iface: *iface, VPSIP: *vps, OnCandidate: handler}
	w.Init()
	if err := sensor.Run(w, *vps); err != nil {
		log.Fatalf("watch-ebpf: %v", err)
	}
}
