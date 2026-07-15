// Package prober — ядро авто-обхода: проверяет доступность цели НАПРЯМУЮ
// (через провайдера) и ЧЕРЕЗ VPS (socks5), и выносит вердикт.
//
//	direct OK               -> "ok"      (не заблокировано, ничего не делаем)
//	direct FAIL + vps OK    -> "blocked" (блок провайдера -> в авто-обход)
//	direct FAIL + vps FAIL  -> "down"    (цель реально недоступна, не трогаем)
//	direct FAIL + vps ERR   -> "unknown" (VPS-проверку не удалось выполнить)
//
// Только чтение сети (исходящие соединения) — компонент безопасен для живого шлюза.
package prober

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// resolve — резолв через системный резолвер (локальный dnscrypt), с таймаутом.
func resolve(host string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

func errStr(err error) string {
	if err == nil {
		return "нет записей"
	}
	return err.Error()
}

type Verdict string

const (
	OK      Verdict = "ok"
	Blocked Verdict = "blocked"
	Down    Verdict = "down"
	Unknown Verdict = "unknown"
)

type Result struct {
	Target  string  `json:"target"`
	Port    int     `json:"port"`
	Verdict Verdict `json:"verdict"`
	Direct  string  `json:"direct"` // ok | текст ошибки
	ViaVPS  string  `json:"via_vps"`
}

type Config struct {
	SocksAddr string        // адрес VPS-socks, напр. 127.0.0.1:1080
	Timeout   time.Duration // на каждую фазу
	TLS       bool          // делать TLS-рукопожатие (порт 443/HTTPS)
}

// Probe: target — домен или IP; sni — имя для TLS (обычно = target).
func Probe(target string, port int, sni string, cfg Config) Result {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if sni == "" {
		sni = target
	}
	r := Result{Target: target, Port: port}

	// 0) резолвим цель заранее (через локальный dnscrypt — без поизонинга).
	// Провал DNS = не можем проверить => "unknown", а не "blocked"/"down".
	ip := target
	if net.ParseIP(target) == nil {
		addrs, err := resolve(target, cfg.Timeout)
		if err != nil || len(addrs) == 0 {
			r.Direct = "dns: " + errStr(err)
			r.Verdict = Unknown
			return r
		}
		ip = addrs[0]
	}

	// 1) прямая проверка — коннект на реальный IP + TLS(SNI) (так DPI режет по SNI)
	if err := reach(directDial, ip, port, sni, cfg); err != nil {
		r.Direct = err.Error()
	} else {
		r.Direct = "ok"
		r.Verdict = OK
		return r // напрямую работает — блока нет
	}

	// 2) провал напрямую -> перепроверяем через VPS
	if cfg.SocksAddr == "" {
		r.Verdict = Unknown
		r.ViaVPS = "socks не задан"
		return r
	}
	dial := func(t string, p int, to time.Duration) (net.Conn, error) {
		return socks5Dial(cfg.SocksAddr, t, p, to)
	}
	if err := reach(dial, target, port, sni, cfg); err != nil {
		r.ViaVPS = err.Error()
		r.Verdict = Down // не работает и там, и там
	} else {
		r.ViaVPS = "ok"
		r.Verdict = Blocked // через VPS работает, напрямую нет -> блок провайдера
	}
	return r
}

type dialFunc func(host string, port int, timeout time.Duration) (net.Conn, error)

func directDial(host string, port int, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
}

// reach: TCP-connect + (опц.) TLS-рукопожатие. Ошибка = недоступно/заблокировано.
func reach(dial dialFunc, host string, port int, sni string, cfg Config) error {
	conn, err := dial(host, port, cfg.Timeout)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	if !cfg.TLS {
		return nil
	}
	conn.SetDeadline(time.Now().Add(cfg.Timeout))
	tc := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	return nil
}

// socks5Dial — минимальный SOCKS5 CONNECT без авторизации (stdlib).
func socks5Dial(socksAddr, host string, port int, timeout time.Duration) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", socksAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("socks dial: %w", err)
	}
	c.SetDeadline(time.Now().Add(timeout))
	ok := func(e error) (net.Conn, error) { c.Close(); return nil, e }

	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil { // ver5, 1 метод, no-auth
		return ok(err)
	}
	g := make([]byte, 2)
	if _, err := io.ReadFull(c, g); err != nil || g[0] != 0x05 || g[1] != 0x00 {
		return ok(fmt.Errorf("socks greeting"))
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))} // connect, atyp=domain
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return ok(err)
	}
	rep := make([]byte, 4)
	if _, err := io.ReadFull(c, rep); err != nil {
		return ok(err)
	}
	if rep[1] != 0x00 {
		return ok(fmt.Errorf("socks connect rep=%d", rep[1]))
	}
	alen := 0
	switch rep[3] {
	case 0x01:
		alen = 4
	case 0x04:
		alen = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return ok(err)
		}
		alen = int(l[0])
	}
	if _, err := io.ReadFull(c, make([]byte, alen+2)); err != nil { // bound addr+port
		return ok(err)
	}
	c.SetDeadline(time.Time{})
	return c, nil
}
