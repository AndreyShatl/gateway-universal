// gateway-detector — компонент авто-обхода (T41).
//
// Сущности:
//   - prober  — проверка цели напрямую vs через VPS -> вердикт (готово);
//   - watcher — пассивный (pcap) детект провалов прямого TCP + SNI (позже);
//   - applier — применение подтверждённого блока (autoroute.json + xray API) (позже).
//
// Пока: CLI-обёртка над prober для проверки/отладки механизма.
//
//	gateway-detector probe <target> [--port 443] [--sni name] [--socks 127.0.0.1:1080] [--no-tls]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"gateway-detector/prober"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "probe" {
		fmt.Fprintln(os.Stderr, "usage: gateway-detector probe <target> [--port N] [--sni name] [--socks addr] [--no-tls]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	port := fs.Int("port", 443, "порт цели")
	sni := fs.String("sni", "", "SNI для TLS (по умолчанию = target)")
	socks := fs.String("socks", "127.0.0.1:1080", "адрес VPS-socks5")
	noTLS := fs.Bool("no-tls", false, "не делать TLS-рукопожатие (просто TCP-connect)")
	timeout := fs.Duration("timeout", 5*time.Second, "таймаут на фазу")
	fs.Parse(os.Args[3:])

	target := os.Args[2]
	res := prober.Probe(target, *port, *sni, prober.Config{
		SocksAddr: *socks, Timeout: *timeout, TLS: !*noTLS,
	})
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}
