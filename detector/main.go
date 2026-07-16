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
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"gateway-detector/applier"
	"gateway-detector/prober"
	"gateway-detector/watcher"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "probe":
		// ниже
	case "watch":
		runWatch()
		return
	default:
		usage()
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

func usage() {
	fmt.Fprintln(os.Stderr, "usage:\n  gateway-detector probe <target> [--port N] [--sni name] [--socks addr] [--no-tls]\n  gateway-detector watch --iface enp2s0 [--vps IP] [--apply]")
	os.Exit(2)
}

func runWatch() {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	iface := fs.String("iface", "", "WAN-интерфейс (авто, если пусто)")
	vps := fs.String("vps", "", "IP VPS — исключить туннель (авто из --config-env, если пусто)")
	configEnv := fs.String("config-env", "/root/gateway-universal/config.env", "config.env для VPS_ADDR")
	socks := fs.String("socks", "127.0.0.1:1080", "VPS-socks5 для перепроверки")
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
	log.Printf("detector: iface=%s vps=%s apply=%v", *iface, *vps, *apply)
	// watcher -> prober (подтверждение) -> [тень: лог | apply: применить]
	handler := func(c watcher.Candidate) {
		target := c.SNI
		if target == "" {
			target = c.DstIP // без SNI (syn-timeout) — проверяем по IP
		}
		port := c.Port
		if port == 0 {
			port = 443
		}
		// с SNI — TLS-рукопожатие (DPI режет по имени); без SNI — только TCP-коннект
		// (блок по IP: SYN дропается, до TLS дело не доходит) на реальном порту.
		res := prober.Probe(target, port, c.SNI, prober.Config{SocksAddr: *socks, Timeout: 6 * time.Second, TLS: c.SNI != ""})
		switch res.Verdict {
		case prober.Blocked:
			if *apply {
				if applier.Apply(target, c.Signal) {
					log.Printf("✅ добавлен в авто-обход: %s (dst=%s)", target, c.DstIP)
				} else {
					log.Printf("… %s подтверждён, но уже в списке / тумблер выкл", target)
				}
			} else {
				log.Printf("🟡 БЫ добавил: %-30s (блок подтверждён; direct=%s)", target, short(res.Direct))
			}
		default:
			log.Printf("⚪ %-30s кандидат, но prober=%s — НЕ добавляю (direct=%s vps=%s)", target, res.Verdict, short(res.Direct), short(res.ViaVPS))
		}
	}
	w := &watcher.Watcher{Iface: *iface, VPSIP: *vps, OnCandidate: handler}
	if err := w.Run(); err != nil {
		log.Fatalf("watch: %v", err)
	}
}

func short(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

func detectIface() string {
	out, err := exec.Command("ip", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	for i, w := range f {
		if w == "dev" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func configVar(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, key+"=") {
			v := strings.TrimPrefix(ln, key+"=")
			v = strings.Trim(strings.Fields(v)[0], `"`)
			return v
		}
	}
	return ""
}

func resolveHost(h string) string {
	if h == "" {
		return ""
	}
	if net.ParseIP(h) != nil {
		return h
	}
	if ips, err := net.LookupHost(h); err == nil {
		for _, ip := range ips {
			if p := net.ParseIP(ip); p != nil && p.To4() != nil {
				return ip
			}
		}
	}
	return ""
}
