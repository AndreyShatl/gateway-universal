// gateway-ui — веб-интерфейс шлюза gateway-universal (каркас, T10).
//
// Только каркас: HTTP-сервер, вход по паролю, сессии, статика из embed.FS,
// /healthz. Бизнес-логики (домены, IP роутера, управление) ещё нет — она
// придёт в T11–T13 и будет ОРКЕСТРИРОВАТЬ существующие скрипты, а не дублировать.
//
// Запуск:
//
//	gateway-ui --listen :8088 --conf /etc/gateway/ui.conf
//
// Пароль: при первом старте, если conf нет, берётся из env GATEWAY_UI_PASSWORD
// и сохраняется (salt:sha256). Без пароля сервер не стартует.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

//go:embed static/*.html
var staticFS embed.FS

// distFSRaw — собранный React/Vite SPA (web-src/, T-shattl-gwui, 2026-08-05),
// заменяет static/dashboard.html как основной интерфейс. Старая панель
// оставлена на /legacy на время обкатки (см. регистрацию маршрутов ниже).
//
//go:embed all:static/dist
var distFSRaw embed.FS

func distFS() fs.FS {
	sub, err := fs.Sub(distFSRaw, "static/dist")
	if err != nil {
		panic("gateway-ui: static/dist embed повреждён: " + err.Error())
	}
	return sub
}

//go:embed strategies.json
var strategiesJSON []byte

const sessionCookie = "gwsess"
const sessionTTL = 12 * time.Hour

type server struct {
	// Пароль. Новый формат — bcrypt-хеш в pwHash (legacy=false).
	// Legacy-формат (старые ui.conf) — salt:sha256hex, legacy=true.
	pwHash string
	salt   string
	legacy bool
	tmpl   *template.Template

	repoDir        string // каталог репозитория (скрипты, шаблон)
	configEnv      string // путь к config.env
	userDomainsDir string // /etc/gateway/domains (домены из UI)
	xrayConfig     string // /opt/xray/config.json (рабочий конфиг)
	xrayBin        string // /opt/xray/xray
	scanDir        string // /etc/gateway/scan (состояние поиска стратегий)
	blockcheck     string // /opt/zapret/blockcheck.sh
	overrides      string // (устар.) оверрайды стратегий
	servicesFile   string // /etc/gateway/zapret-services.json (динамические сервисы)
	connsFile      string // /etc/gateway/connections.json (сохранённые VPS-хосты)
	dbPath         string // /etc/gateway/gateway.db (whitelist+strategies/services/history, доступ через scripts/gwdb.py)
	autorouteFile  string // /etc/gateway/autoroute.json (список авто-обхода)
	recheckFile    string // /etc/gateway/recheck.json (расписание перепроверки авто-обхода)
	ver            string // версия сборки (mtime бинаря) — для автоперезагрузки вкладки
	timeline       *timelineLog
	orchestrator   *orchestrator

	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func main() {
	listen := flag.String("listen", ":8088", "адрес прослушивания (host:port)")
	conf := flag.String("conf", "/etc/gateway/ui.conf", "файл с паролем (salt:hash)")
	repo := flag.String("repo", "/root/gateway-universal", "каталог репозитория на устройстве")
	configEnv := flag.String("config-env", "", "путь к config.env (default <repo>/config.env)")
	userDomains := flag.String("user-domains-dir", "/etc/gateway/domains", "каталог пользовательских доменов")
	xrayConfig := flag.String("xray-config", "/opt/xray/config.json", "рабочий config.json")
	xrayBin := flag.String("xray-bin", "/opt/xray/xray", "бинарник xray")
	scanDir := flag.String("scan-dir", "/etc/gateway/scan", "каталог состояния поиска стратегий")
	blockcheck := flag.String("blockcheck", "/opt/zapret/blockcheck.sh", "путь к blockcheck.sh")
	overrides := flag.String("zapret-overrides", "/etc/gateway/zapret-overrides.env", "(устар.) файл оверрайдов")
	servicesFile := flag.String("zapret-services", "/etc/gateway/zapret-services.json", "файл сервисов zapret")
	connsFile := flag.String("connections", "/etc/gateway/connections.json", "файл сохранённых VPS-хостов")
	dbPath := flag.String("db", "/etc/gateway/gateway.db", "БД whitelist+strategies (T48)")
	autorouteFile := flag.String("autoroute-file", "/etc/gateway/autoroute.json", "файл списка авто-обхода")
	recheckFile := flag.String("recheck-file", "/etc/gateway/recheck.json", "файл расписания перепроверки авто-обхода")
	timelineFile := flag.String("timeline-file", "/etc/gateway/timeline.jsonl", "файл журнала событий Mission Timeline")
	initPwd := flag.Bool("init-password", false, "создать ui.conf из env GATEWAY_UI_PASSWORD и выйти")
	flag.Parse()

	if *configEnv == "" {
		*configEnv = filepath.Join(*repo, "config.env")
	}
	s := &server{
		sessions: map[string]time.Time{}, repoDir: *repo, configEnv: *configEnv,
		userDomainsDir: *userDomains, xrayConfig: *xrayConfig, xrayBin: *xrayBin,
		scanDir: *scanDir, blockcheck: *blockcheck, overrides: *overrides, servicesFile: *servicesFile,
		connsFile: *connsFile, dbPath: *dbPath,
		autorouteFile: *autorouteFile, recheckFile: *recheckFile,
		timeline: newTimelineLog(*timelineFile),
	}
	s.orchestrator = newOrchestrator(s.timeline, s.configEnv)
	s.ver = buildVersion()
	if err := s.loadOrInitPassword(*conf); err != nil {
		log.Fatalf("gateway-ui: %v", err)
	}
	if *initPwd {
		log.Printf("ui.conf готов (%s) — выходим", *conf)
		return
	}
	s.tmpl = template.Must(template.ParseFS(staticFS, "static/*.html"))
	if err := s.initGWDB(); err != nil {
		log.Printf("gateway-ui: initGWDB: %v", err)
	}
	s.ensureAutorouteInfra()
	// syncAutoroute при старте — восстанавливает состояние детектора после ребута
	// (gateway-detector.service НЕ boot-enabled намеренно, им управляет только
	// тумблер «Авто-обход» через systemctl start/stop, см. autoroute.go). Без
	// этого вызова после ребута детектор остаётся выключенным, даже если тумблер
	// был включён — ipset/iptables-инфра восстановится (ensureAutorouteInfra
	// выше), а сам процесс пополнения списка — нет.
	// В ГОРУТИНЕ: syncAutoroute резолвит КАЖДЫЙ домен списка последовательно
	// (до 4с таймаут на домен) — при сотнях записей это блокировало бы старт
	// HTTP-сервера на минуты (замечено при ребут-тесте: :8088 не слушал 47+с).
	go s.syncAutoroute(s.readAutoRoute())

	// system.boot — proxy для "физической" загрузки шлюза: gateway-ui сама
	// стартует через systemd вместе с системой при ребуте (не boot-enabled
	// таймер, а постоянный сервис), поэтому старт процесса — надёжный сигнал
	// "устройство только что загрузилось" для ленты Mission Timeline.
	s.timeline.Record("system.boot", "System boot completed")
	go s.vpnWatchLoop()
	go s.dnsWatchLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/api/ping", s.requireAuth(s.handlePing))
	mux.HandleFunc("/api/hostname", s.requireAuth(s.handleHostname))
	mux.HandleFunc("/api/router-ip", s.requireAuth(s.handleRouterIP))
	mux.HandleFunc("/api/connection", s.requireAuth(s.handleConnection))
	mux.HandleFunc("/api/connections", s.requireAuth(s.handleConnections))
	mux.HandleFunc("/api/domains", s.requireAuth(s.handleDomains))
	mux.HandleFunc("/api/zapret", s.requireAuth(s.handleZapret))
	mux.HandleFunc("/api/zapret/services", s.requireAuth(s.handleServices))
	mux.HandleFunc("/api/strategies", s.requireAuth(s.handleStrategies))
	mux.HandleFunc("/api/zapret/version", s.requireAuth(s.handleZapretVersion))
	mux.HandleFunc("/api/zapret/update", s.requireAuth(s.handleZapretUpdate))
	mux.HandleFunc("/api/ciadpi/version", s.requireAuth(s.handleCiadpiVersion))
	mux.HandleFunc("/api/ciadpi/update", s.requireAuth(s.handleCiadpiUpdate))
	mux.HandleFunc("/api/zapret2/version", s.requireAuth(s.handleZapret2Version))
	mux.HandleFunc("/api/zapret2/update", s.requireAuth(s.handleZapret2Update))
	mux.HandleFunc("/api/scan", s.requireAuth(s.handleScan))
	mux.HandleFunc("/api/scan/start", s.requireAuth(s.handleScanStart))
	mux.HandleFunc("/api/scan/stop", s.requireAuth(s.handleScanStop))
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/exit-ip", s.requireAuth(s.handleExitIP))
	mux.HandleFunc("/api/restart", s.requireAuth(s.handleRestart))
	mux.HandleFunc("/api/smoke", s.requireAuth(s.handleSmoke))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/api/whitelist", s.requireAuth(s.handleWhitelist))
	mux.HandleFunc("/api/presets", s.requireAuth(s.handlePresets))
	mux.HandleFunc("/api/game-mode", s.requireAuth(s.handleGameMode))
	mux.HandleFunc("/api/vps-mode", s.requireAuth(s.handleVPSMode))
	mux.HandleFunc("/api/adguard", s.requireAuth(s.handleAdguard))
	mux.HandleFunc("/api/autoroute", s.requireAuth(s.handleAutoRoute))
	mux.HandleFunc("/api/monitor", s.requireAuth(s.handleMonitor))
	mux.HandleFunc("/api/vps-domains", s.requireAuth(s.handleVPSDomains))
	mux.HandleFunc("/api/recheck", s.requireAuth(s.handleRecheck))
	mux.HandleFunc("/api/nightly-progress", s.requireAuth(s.handleNightlyProgress))
	mux.HandleFunc("/api/nightly-trigger", s.requireAuth(s.handleNightlyTrigger))
	mux.HandleFunc("/api/timeline", s.requireAuth(s.handleTimeline))
	mux.HandleFunc("/api/engine/snapshots", s.requireAuth(s.handleEngineSnapshots))
	mux.HandleFunc("/api/engine/status", s.requireAuth(s.handleEngineStatus))

	// Traffic Engine Orchestrator API (ТЗ п.15) — см. traffic_engine_api.go.
	mux.HandleFunc("GET /api/traffic-engine", s.requireAuth(s.handleTrafficEngineList))
	mux.HandleFunc("GET /api/traffic-engine/status", s.requireAuth(s.handleTrafficEngineStatus))
	mux.HandleFunc("GET /api/traffic-engine/available", s.requireAuth(s.handleTrafficEngineAvailable))
	mux.HandleFunc("POST /api/traffic-engine/install", s.requireAuth(s.handleTrafficEngineInstall))
	mux.HandleFunc("POST /api/traffic-engine/uninstall", s.requireAuth(s.handleTrafficEngineUninstall))
	mux.HandleFunc("POST /api/traffic-engine/start", s.requireAuth(s.handleTrafficEngineControl("start")))
	mux.HandleFunc("POST /api/traffic-engine/stop", s.requireAuth(s.handleTrafficEngineControl("stop")))
	mux.HandleFunc("POST /api/traffic-engine/restart", s.requireAuth(s.handleTrafficEngineControl("restart")))
	mux.HandleFunc("POST /api/traffic-engine/switch", s.requireAuth(s.handleTrafficEngineSwitch))
	mux.HandleFunc("GET /api/traffic-engine/config", s.requireAuth(s.handleTrafficEngineConfigGet))
	mux.HandleFunc("PUT /api/traffic-engine/config", s.requireAuth(s.handleTrafficEngineConfigPut))
	mux.HandleFunc("POST /api/traffic-engine/health-check", s.requireAuth(s.handleTrafficEngineHealthCheck))
	mux.HandleFunc("GET /api/traffic-engine/history", s.requireAuth(s.handleTrafficEngineHistory))
	mux.HandleFunc("GET /api/traffic-engine/backups", s.requireAuth(s.handleTrafficEngineBackups))
	mux.HandleFunc("POST /api/traffic-engine/rollback", s.requireAuth(s.handleTrafficEngineRollback))
	mux.HandleFunc("/api/host-metrics", s.requireAuth(s.handleHostMetrics))
	mux.HandleFunc("/api/services/detail", s.requireAuth(s.handleServicesDetail))
	mux.HandleFunc("/api/services/stop", s.requireAuth(s.handleStop))
	mux.HandleFunc("/ws/console", s.requireAuth(s.handleConsole))
	mux.HandleFunc("/api/network", s.requireAuth(s.handleNetwork))
	mux.HandleFunc("/api/internet-checks", s.requireAuth(s.handleInternetChecks))
	mux.HandleFunc("/api/gmp-status", s.requireAuth(s.handleGMPStatus))

	// T-shattl-gwui (2026-08-05): React/Vite SPA — новый интерфейс. Старая
	// панель (dashboard.html) оставлена на /legacy на время обкатки, ссылка на
	// неё есть в заглушках ComingSoon непортированных разделов SPA.
	dist := distFS()
	staticServer := http.FileServerFS(dist)
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		staticServer.ServeHTTP(w, r)
	})
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		staticServer.ServeHTTP(w, r)
	})
	indexHTML, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		log.Fatalf("gateway-ui: не удалось прочитать встроенный static/dist/index.html: %v", err)
	}
	mux.HandleFunc("/legacy", s.requireAuth(s.handleDashboard))
	// GET "/" — subtree-паттерн в net/http.ServeMux: ловит и "/", и клиентские
	// маршруты react-router (/domains, /whitelist, /monitor, /logs, /settings),
	// у которых на сервере нет отдельного обработчика.
	mux.HandleFunc("/", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	}))

	srv := &http.Server{
		Addr:         *listen,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	log.Printf("gateway-ui слушает %s", *listen)
	log.Fatal(srv.ListenAndServe())
}

// ---- пароль -----------------------------------------------------------

func (s *server) loadOrInitPassword(path string) error {
	data, err := os.ReadFile(path)
	if err == nil {
		line := strings.TrimSpace(string(data))
		switch {
		case strings.HasPrefix(line, "$2"): // bcrypt
			s.pwHash, s.legacy = line, false
		case strings.Contains(line, ":"): // legacy salt:sha256
			parts := strings.SplitN(line, ":", 2)
			if parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("повреждён %s", path)
			}
			s.salt, s.pwHash, s.legacy = parts[0], parts[1], true
		default:
			return fmt.Errorf("повреждён %s (неизвестный формат)", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("чтение %s: %w", path, err)
	}
	// Первый запуск — пароль из env, сохраняем как bcrypt
	pw := os.Getenv("GATEWAY_UI_PASSWORD")
	if pw == "" {
		return fmt.Errorf("нет %s и не задан GATEWAY_UI_PASSWORD — пароль не настроен", path)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.pwHash, s.legacy = string(h), false
	if err := os.WriteFile(path, append(h, '\n'), 0o600); err != nil {
		return fmt.Errorf("запись %s: %w", path, err)
	}
	log.Printf("пароль инициализирован (bcrypt), сохранён в %s", path)
	return nil
}

func (s *server) checkPassword(pw string) bool {
	if s.legacy { // старый sha256(salt+pw)
		sum := sha256.Sum256([]byte(s.salt + pw))
		return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(s.pwHash)) == 1
	}
	return bcrypt.CompareHashAndPassword([]byte(s.pwHash), []byte(pw)) == nil
}

// ---- сессии -----------------------------------------------------------

func (s *server) newSession() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return tok
}

func (s *server) validSession(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, tok)
		return false
	}
	return true
}

// fwdPrefix — T-shattl-tunnel (2026-08-12): gateway-ui отдаётся и напрямую
// (basePath=""), и через дашборд-прокси (/gw/<id>/...) — reverse-proxy на
// сервере ставит этот заголовок (см. gmp-server/internal/api/gwproxy.go),
// без него все абсолютные редиректы (/login, /) увели бы браузер на корень
// ДАШБОРДА вместо самого gateway-ui.
func fwdPrefix(r *http.Request) string {
	return r.Header.Get("X-Forwarded-Prefix")
}

// cookiePath — T-shattl-cookie-collision (2026-08-12): без префикса кука
// сессии ставится с Path=/ на общем домене дашборда (example.gateway-dashboard.tld) —
// при одновременно открытых вкладках разных шлюзов (оба проксируются под
// /gw/<id>/ на ТОМ ЖЕ origin) последняя авторизация перезаписывала куку
// ПРЕДЫДУЩЕГО шлюза (тот же Name+Domain, любой Path перекрывает), а браузер
// заодно предлагал "тот же" сохранённый пароль для обоих шлюзов (autofill
// матчится по origin, не по path). Path=<префикс>/ скоупит куку строго под
// конкретный шлюз — при прямом доступе (LAN, префикса нет) ничего не меняется.
func cookiePath(r *http.Request) string {
	if p := fwdPrefix(r); p != "" {
		return p + "/"
	}
	return "/"
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.validSession(c.Value) {
			http.Redirect(w, r, fwdPrefix(r)+"/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// ---- хендлеры ---------------------------------------------------------

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	prefix := fwdPrefix(r)
	// hostname — не просто заголовок; T-shattl-cookie-collision (2026-08-12):
	// та же форма логина везде идентична (один password-инпут без имени
	// пользователя), браузер сопоставляет сохранённые пароли по origin, а
	// у всех шлюзов за прокси origin ОДИН (example.gateway-dashboard.tld) — password
	// manager путал/переиспользовал пароль одного шлюза для другого. Скрытое
	// поле username=hostname — стандартный приём, чтобы разные шлюзы
	// сохранялись как разные записи.
	hostname, _ := os.Hostname()
	if r.Method == http.MethodGet {
		s.render(w, "login.html", map[string]any{"Prefix": prefix, "Hostname": hostname})
		return
	}
	if !s.checkPassword(r.FormValue("password")) {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login.html", map[string]any{"Error": "Неверный пароль", "Prefix": prefix, "Hostname": hostname})
		return
	}
	tok := s.newSession()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: cookiePath(r),
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
	http.Redirect(w, r, prefix+"/", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: cookiePath(r), MaxAge: -1})
	http.Redirect(w, r, fwdPrefix(r)+"/login", http.StatusSeeOther)
}

func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, "dashboard.html", map[string]any{"Ver": s.ver, "Prefix": fwdPrefix(r)})
}

// buildVersion — метка версии бинаря (mtime исполняемого файла). Меняется при
// каждом деплое; вкладка сравнивает её с загруженной и перезагружается сама.
func buildVersion() string {
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			return fmt.Sprintf("%d", fi.ModTime().Unix())
		}
	}
	return fmt.Sprintf("%d", time.Now().Unix())
}

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// handleHostname — T-shattl-multi-gateway (2026-08-12): имя шлюза в шапке
// локального UI — при нескольких открытых вкладках разных шлюзов (обычное
// дело теперь, когда каждый шлюз доступен через дашборд-прокси) не путать,
// какая вкладка какому шлюзу принадлежит.
func (s *server) handleHostname(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	writeJSON(w, http.StatusOK, map[string]string{"hostname": hostname})
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// HTML/JS встроены в бинарь — после деплоя страница должна обновляться
	// сразу, без залипания старого JS в открытой вкладке.
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
