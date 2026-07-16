// Package applier — применяет подтверждённый блок в авто-обход БЕЗ рестарта xray:
// добавляет цель в общий autoroute.json (единый источник, что и у gateway-ui) под
// файловой блокировкой, затем ipset add (мгновенный эффект через выделенный
// inbound :12347 → proxy-mux). Домены резолвятся в IP (через локальный dnscrypt).
package applier

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	IPSet   = "gw_autoroute"
	LAN     = "192.168.0.0/16"
	Port    = "12347"        // dokodemo autoroute-in (TCP REDIRECT)
	UDPPort = "12346"        // существующий tproxy-udp inbound -> proxy-mux
	Mark    = "0x1/0xffffffff"
	File    = "/etc/gateway/autoroute.json"
)

type store struct {
	Enabled bool    `json:"enabled"`
	Entries []entry `json:"entries"`
}

// entry — адрес + метаданные (когда/чем добавлен). Совместимо со схемой gateway-ui.
type entry struct {
	Addr   string `json:"addr"`
	Added  string `json:"added,omitempty"`
	Source string `json:"source,omitempty"`
}

// UnmarshalJSON — принимает и объект, и старую строку (миграция формата).
func (e *entry) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		e.Addr, e.Source = s, "legacy"
		return nil
	}
	type raw entry
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*e = entry(r)
	return nil
}

// EnsureInfra — ipset + iptables-редирект (идемпотентно). Дублирует то, что делает
// gateway-ui: детектор может добавлять до того, как gateway-ui это создал.
func EnsureInfra() {
	run("ipset", "create", IPSet, "hash:net", "family", "inet", "-exist")
	// TCP: все порты (не только 80,443) -> REDIRECT на dokodemo autoroute-in.
	ensureRule("nat", []string{"-s", LAN, "-p", "tcp",
		"-m", "set", "--match-set", IPSet, "dst", "-j", "REDIRECT", "--to-ports", Port})
	// UDP: TPROXY на существующий tproxy-udp :12346 (-> proxy-mux). Маршрут
	// fwmark 0x1 -> table 100 -> lo уже настроен основным tproxy-потоком.
	ensureRule("mangle", []string{"-s", LAN, "-p", "udp",
		"-m", "set", "--match-set", IPSet, "dst",
		"-j", "TPROXY", "--on-port", UDPPort, "--on-ip", "0.0.0.0", "--tproxy-mark", Mark})
}

// ensureRule — идемпотентно вставить правило в начало PREROUTING (проверка -C).
func ensureRule(table string, spec []string) {
	if run("iptables", append([]string{"-t", table, "-C", "PREROUTING"}, spec...)...) != nil {
		run("iptables", append([]string{"-t", table, "-I", "PREROUTING", "1"}, spec...)...)
	}
}

// Apply — добавить цель (домен или IP) в авто-обход. source — чем поймано
// (имя сигнала). Возвращает true, если реально добавлено (не было раньше и вкл).
func Apply(target, source string) bool {
	f, err := os.OpenFile(File, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	var s store
	data, _ := os.ReadFile(File)
	json.Unmarshal(data, &s)
	if !s.Enabled {
		return false // тумблер выключен — не трогаем
	}
	for _, e := range s.Entries {
		if e.Addr == target {
			return false // уже есть
		}
	}
	s.Entries = append(s.Entries, entry{
		Addr:   target,
		Added:  time.Now().UTC().Format(time.RFC3339),
		Source: source,
	})
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := File + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, File)
	}

	// применить в ipset
	EnsureInfra()
	if net.ParseIP(target) != nil || strings.Contains(target, "/") {
		run("ipset", "add", IPSet, target, "-exist")
	} else {
		for _, ip := range resolveV4(target) {
			run("ipset", "add", IPSet, ip, "-exist")
		}
	}
	return true
}

func run(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func resolveV4(host string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	addrs, _ := net.DefaultResolver.LookupHost(ctx, host)
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	return v4
}
