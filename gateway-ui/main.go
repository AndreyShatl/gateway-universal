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
	overrides      string // /etc/gateway/zapret-overrides.env (оверрайды стратегий)

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
	overrides := flag.String("zapret-overrides", "/etc/gateway/zapret-overrides.env", "файл оверрайдов стратегий zapret")
	initPwd := flag.Bool("init-password", false, "создать ui.conf из env GATEWAY_UI_PASSWORD и выйти")
	flag.Parse()

	if *configEnv == "" {
		*configEnv = filepath.Join(*repo, "config.env")
	}
	s := &server{
		sessions: map[string]time.Time{}, repoDir: *repo, configEnv: *configEnv,
		userDomainsDir: *userDomains, xrayConfig: *xrayConfig, xrayBin: *xrayBin,
		scanDir: *scanDir, blockcheck: *blockcheck, overrides: *overrides,
	}
	if err := s.loadOrInitPassword(*conf); err != nil {
		log.Fatalf("gateway-ui: %v", err)
	}
	if *initPwd {
		log.Printf("ui.conf готов (%s) — выходим", *conf)
		return
	}
	s.tmpl = template.Must(template.ParseFS(staticFS, "static/*.html"))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/api/ping", s.requireAuth(s.handlePing))
	mux.HandleFunc("/api/router-ip", s.requireAuth(s.handleRouterIP))
	mux.HandleFunc("/api/connection", s.requireAuth(s.handleConnection))
	mux.HandleFunc("/api/domains", s.requireAuth(s.handleDomains))
	mux.HandleFunc("/api/zapret", s.requireAuth(s.handleZapret))
	mux.HandleFunc("/api/zapret/strategies", s.requireAuth(s.handleStrategies))
	mux.HandleFunc("/api/zapret/strategy", s.requireAuth(s.handleStrategySet))
	mux.HandleFunc("/api/zapret/version", s.requireAuth(s.handleZapretVersion))
	mux.HandleFunc("/api/zapret/update", s.requireAuth(s.handleZapretUpdate))
	mux.HandleFunc("/api/scan", s.requireAuth(s.handleScan))
	mux.HandleFunc("/api/scan/start", s.requireAuth(s.handleScanStart))
	mux.HandleFunc("/api/scan/stop", s.requireAuth(s.handleScanStop))
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/exit-ip", s.requireAuth(s.handleExitIP))
	mux.HandleFunc("/api/restart", s.requireAuth(s.handleRestart))
	mux.HandleFunc("/api/smoke", s.requireAuth(s.handleSmoke))
	mux.HandleFunc("/api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("/", s.requireAuth(s.handleDashboard))

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

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.validSession(c.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// ---- хендлеры ---------------------------------------------------------

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, "login.html", nil)
		return
	}
	if !s.checkPassword(r.FormValue("password")) {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login.html", map[string]any{"Error": "Неверный пароль"})
		return
	}
	tok := s.newSession()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, "dashboard.html", nil)
}

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
