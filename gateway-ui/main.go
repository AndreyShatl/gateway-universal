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
)

//go:embed static/*.html
var staticFS embed.FS

const sessionCookie = "gwsess"
const sessionTTL = 12 * time.Hour

type server struct {
	salt, hash string // пароль: salt + sha256hex(salt+password)
	tmpl       *template.Template

	repoDir        string // каталог репозитория (скрипты, шаблон)
	configEnv      string // путь к config.env
	userDomainsDir string // /etc/gateway/domains (домены из UI)
	xrayConfig     string // /opt/xray/config.json (рабочий конфиг)
	xrayBin        string // /opt/xray/xray

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
	flag.Parse()

	if *configEnv == "" {
		*configEnv = filepath.Join(*repo, "config.env")
	}
	s := &server{
		sessions: map[string]time.Time{}, repoDir: *repo, configEnv: *configEnv,
		userDomainsDir: *userDomains, xrayConfig: *xrayConfig, xrayBin: *xrayBin,
	}
	if err := s.loadOrInitPassword(*conf); err != nil {
		log.Fatalf("gateway-ui: %v", err)
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
	mux.HandleFunc("/api/domains", s.requireAuth(s.handleDomains))
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
		parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("повреждён %s (ожидается salt:hash)", path)
		}
		s.salt, s.hash = parts[0], parts[1]
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("чтение %s: %w", path, err)
	}
	// Первый запуск — пароль из env
	pw := os.Getenv("GATEWAY_UI_PASSWORD")
	if pw == "" {
		return fmt.Errorf("нет %s и не задан GATEWAY_UI_PASSWORD — пароль не настроен", path)
	}
	saltb := make([]byte, 16)
	if _, err := rand.Read(saltb); err != nil {
		return err
	}
	s.salt = hex.EncodeToString(saltb)
	s.hash = hashPassword(s.salt, pw)
	if err := os.WriteFile(path, []byte(s.salt+":"+s.hash+"\n"), 0o600); err != nil {
		return fmt.Errorf("запись %s: %w", path, err)
	}
	log.Printf("пароль инициализирован, сохранён в %s", path)
	return nil
}

func hashPassword(salt, pw string) string {
	sum := sha256.Sum256([]byte(salt + pw))
	return hex.EncodeToString(sum[:])
}

func (s *server) checkPassword(pw string) bool {
	want := hashPassword(s.salt, pw)
	return subtle.ConstantTimeCompare([]byte(want), []byte(s.hash)) == 1
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
