// network.go — Network-страница (ТЗ Shattl Gateway UI Specification,
// 2026-08-05): интерфейсы, маршрутизация, DNS. Читает то, что реально есть
// на хосте (/proc/net/dev, /sys/class/net), не хардкодит eth0/wg0 — этот
// конкретный шлюз однопортовый (см. находку T-shattl-dns-flap: единственный
// enp2s0), WireGuard в проекте не используется вообще (см. GMP-Agent —
// исключён из протокола, "в РФ душится").
package main

import (
	"bufio"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type netInterface struct {
	Name      string `json:"name"`
	Up        bool   `json:"up"`
	SpeedMbps int    `json:"speed_mbps"`
	RXBytes   uint64 `json:"rx_bytes"`
	TXBytes   uint64 `json:"tx_bytes"`
	RXErrors  uint64 `json:"rx_errors"`
	TXErrors  uint64 `json:"tx_errors"`
	RXDropped uint64 `json:"rx_dropped"`
	TXDropped uint64 `json:"tx_dropped"`
}

type networkResponse struct {
	Interfaces   []netInterface `json:"interfaces"`
	DefaultRoute string         `json:"default_route"` // "via X dev Y"
	DNSServers   []string       `json:"dns_servers"`
	LANIP        string         `json:"lan_ip"`
}

var reRouteVia = regexp.MustCompile(`via (\S+) dev (\S+)`)

func (s *server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, networkResponse{
		Interfaces:   readNetInterfaces(),
		DefaultRoute: readDefaultRoute(),
		DNSServers:   readDNSServers(),
		LANIP:        readLANIP(),
	})
}

func readNetInterfaces() []netInterface {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return []netInterface{}
	}
	defer f.Close()

	out := []netInterface{}
	sc := bufio.NewScanner(f)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue // 2 строки заголовка
		}
		line := sc.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rxBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		rxErrors, _ := strconv.ParseUint(fields[2], 10, 64)
		rxDropped, _ := strconv.ParseUint(fields[3], 10, 64)
		txBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		txErrors, _ := strconv.ParseUint(fields[10], 10, 64)
		txDropped, _ := strconv.ParseUint(fields[11], 10, 64)

		out = append(out, netInterface{
			Name:      name,
			Up:        readOperstate(name) == "up",
			SpeedMbps: readSpeed(name),
			RXBytes:   rxBytes,
			TXBytes:   txBytes,
			RXErrors:  rxErrors,
			TXErrors:  txErrors,
			RXDropped: rxDropped,
			TXDropped: txDropped,
		})
	}
	return out
}

func readOperstate(name string) string {
	raw, err := os.ReadFile(filepath.Join("/sys/class/net", name, "operstate"))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

// readSpeed — Мбит/с из /sys/class/net/<if>/speed. Для loopback и down-
// интерфейсов файл либо отсутствует, либо возвращает -1/ошибку — оба случая
// молча дают 0, не ошибку (не все интерфейсы вообще имеют физическую скорость).
func readSpeed(name string) int {
	raw, err := os.ReadFile(filepath.Join("/sys/class/net", name, "speed"))
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func readDefaultRoute() string {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	m := reRouteVia.FindStringSubmatch(line)
	if len(m) != 3 {
		return line
	}
	return "via " + m[1] + " dev " + m[2]
}

func readDNSServers() []string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return []string{}
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == "nameserver" {
			out = append(out, fields[1])
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func readLANIP() string {
	out, err := exec.Command("ip", "route", "get", "1.1.1.1").Output()
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`src (\S+)`)
	m := re.FindStringSubmatch(string(out))
	if len(m) != 2 {
		return ""
	}
	return m[1]
}
