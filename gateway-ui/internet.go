// internet.go — живые чеки связности для панели "Internet" (та же идея, что
// и на Dashboard gmp-server: DNS/HTTPS/локальный роутер/VPS — независимые
// сигналы, "VPN жив, а DNS уже умер" бывает, ни один чек не подменяет
// остальные). Независимая копия того же подхода, что в gmp-agent/internal/
// netinfo (тот же автор/репо) — та версия недоступна отсюда, локальная
// панель не должна зависеть от отдельного процесса gmp-agent.
package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

type internetChecks struct {
	DNSOk          bool `json:"dns_ok"`
	HTTPSOk        bool `json:"https_ok"`
	LocalGatewayOk bool `json:"local_gateway_ok"`
	VPSOk          bool `json:"vps_ok"`
}

// Тот же таймаут, что и в gmp-agent/internal/netinfo (httpTimeout) — короче
// рисковал давать ложный "Down" на холодном старте (пустой DNS/TLS-кэш).
const internetCheckTimeout = 5 * time.Second

func (s *server) handleInternetChecks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, internetChecks{
		DNSOk:          checkDNS(),
		HTTPSOk:        checkHTTPS(),
		LocalGatewayOk: checkLocalGateway(),
		VPSOk:          checkVPS(s),
	})
}

func checkDNS() bool {
	ctx, cancel := context.WithTimeout(context.Background(), internetCheckTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, "cloudflare.com")
	return err == nil && len(addrs) > 0
}

func checkHTTPS() bool {
	client := &http.Client{Timeout: internetCheckTimeout}
	req, err := http.NewRequest(http.MethodGet, "https://cloudflare.com/generate_204", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true // любой ответ сервера — HTTPS в принципе работает, код не важен
}

// checkLocalGateway — TCP-коннект до роутера дома (default route), не ICMP
// (не требует root). Connection refused (порт закрыт) — хост ОТВЕТИЛ, значит
// жив, тоже засчитываем reachable; не отвечает вообще — единственный признак
// "недоступен".
func checkLocalGateway() bool {
	route := readDefaultRoute()
	m := reRouteVia.FindStringSubmatch(route)
	if len(m) != 3 {
		return false
	}
	return tcpProbe(m[1] + ":80")
}

// checkVPS — то же самое до сконфигурированного VPS (VPS_ADDR:VPS_PORT_GRPC
// из config.env) — если VPS вообще не настроен, считается недоступным
// (нет VPN — нет и VPS-соединения), не ошибкой.
func checkVPS(s *server) bool {
	addr, _ := readConfigVar(s.configEnv, "VPS_ADDR")
	port, _ := readConfigVar(s.configEnv, "VPS_PORT_GRPC")
	if addr == "" || port == "" {
		return false
	}
	return tcpProbe(addr + ":" + port)
}

// tcpProbe — "жив ли хост" без ICMP/root: TCP-коннект. Connection refused
// (порт закрыт) — хост ОТВЕТИЛ, значит жив, тоже засчитываем reachable;
// не отвечает вообще (таймаут/no route) — единственный признак "недоступен".
func tcpProbe(hostPort string) bool {
	conn, err := net.DialTimeout("tcp", hostPort, internetCheckTimeout)
	if err == nil {
		conn.Close()
		return true
	}
	return strings.Contains(err.Error(), "refused")
}
