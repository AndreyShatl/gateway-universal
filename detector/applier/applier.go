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
	IPSet = "gw_autoroute"
	LAN   = "192.168.0.0/16"
	Port  = "12347"
	File  = "/etc/gateway/autoroute.json"
)

type store struct {
	Enabled bool     `json:"enabled"`
	Entries []string `json:"entries"`
}

// EnsureInfra — ipset + iptables-редирект (идемпотентно). Дублирует то, что делает
// gateway-ui: детектор может добавлять до того, как gateway-ui это создал.
func EnsureInfra() {
	run("ipset", "create", IPSet, "hash:net", "family", "inet", "-exist")
	// все TCP-порты (не только 80,443) — для игровых серверов, блокируемых по IP.
	match := []string{"-s", LAN, "-p", "tcp",
		"-m", "set", "--match-set", IPSet, "dst", "-j", "REDIRECT", "--to-ports", Port}
	if run("iptables", append([]string{"-t", "nat", "-C", "PREROUTING"}, match...)...) != nil {
		run("iptables", append([]string{"-t", "nat", "-I", "PREROUTING", "1"}, match...)...)
	}
}

// Apply — добавить цель (домен или IP) в авто-обход. Возвращает true, если реально
// добавлено (не было раньше и авто-обход включён).
func Apply(target string) bool {
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
		if e == target {
			return false // уже есть
		}
	}
	s.Entries = append(s.Entries, target)
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
