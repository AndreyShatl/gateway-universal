package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Zapret (T20): показать реально работающие стратегии DPI-обхода и домены под них.
// Источник истины — то, что РЕАЛЬНО запущено: командные строки nfqws (pgrep -a).
// Внутри одного процесса стратегии разделены флагом --new. Только чтение.

type zStrategy struct {
	Queue   string   `json:"queue"`
	Proto   string   `json:"proto"`   // tcp / udp
	Ports   string   `json:"ports"`   // из --filter-tcp/udp
	L7      string   `json:"l7"`      // из --filter-l7 (discord,stun)
	Desync  string   `json:"desync"`  // --dpi-desync=...
	Repeats string   `json:"repeats"` // --dpi-desync-repeats
	Fooling string   `json:"fooling"` // --dpi-desync-fooling
	List    string   `json:"list"`    // имя hostlist-файла
	Domains []string `json:"domains"` // домены из hostlist
}

// Блоки стратегий zapret.sh: id → переменная DESYNC_<VAR> + калиброванный дефолт.
// Оверрайды живут в /etc/gateway/zapret-overrides.env (DESYNC_<VAR>="...").
// Сервис zapret: имя, домены (inline) и каналы (tcp/udp с портами+desync).
type zChannel struct {
	Proto  string `json:"proto"`
	Ports  string `json:"ports"`
	L7     string `json:"l7,omitempty"`
	Desync string `json:"desync"`
}
type zService struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Featured bool       `json:"featured"`
	Mode     string     `json:"mode"` // vps | zapret (пусто = zapret)
	Domains  []string   `json:"domains"`
	Channels []zChannel `json:"channels"`
	// AutoAt — когда режим последний раз подобран кнопкой "auto" (blockcheck
	// + majority vote), не вручную. Пусто, если mode когда-либо менялся
	// вручную после этого — см. фидбек 2026-08-07 ("auto" не персистентный
	// режим, это разовое действие, но должно быть видно, что оно было).
	AutoAt string `json:"auto_at,omitempty"`
}

func (s *server) readServices() ([]zService, error) {
	data, err := os.ReadFile(s.servicesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []zService{}, nil
		}
		return nil, err
	}
	var svc []zService
	if err := json.Unmarshal(data, &svc); err != nil {
		return nil, err
	}
	return svc, nil
}

// handleServices — GET список сервисов; POST — заменить весь список и рестартить zapret.
func (s *server) handleServices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		svc, err := s.readServices()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"services": svc})

	case http.MethodPost:
		var svc []zService
		if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "плохой JSON"})
			return
		}
		if msg := validateServices(svc); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
			return
		}
		// T-vps-pin (2026-08-16, свод в один путь): для отслеживания переходов
		// mode -> "vps" / mode "vps" -> что-то ещё нужен старый список ДО
		// перезаписи файла — иначе не с чем сравнивать после os.Rename ниже.
		oldSvc, _ := s.readServices()
		oldMode := map[string]string{}
		for _, v := range oldSvc {
			oldMode[v.ID] = v.Mode
		}
		b, _ := json.MarshalIndent(svc, "", "  ")
		if err := os.MkdirAll(filepath.Dir(s.servicesFile), 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		tmp := s.servicesFile + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		os.Rename(tmp, s.servicesFile)
		// Перегенерировать xray-конфиг: домены, отданные в zapret, исключаются из
		// VPS-роутинга (build-domains вычитает их). Затем рестарт xray и zapret.
		if out, err := s.runScript("xray/render-config.sh",
			"--template", filepath.Join(s.repoDir, "xray", "config.template.json"),
			"--out", s.xrayConfig, "--config", s.configEnv, "--xray", s.xrayBin,
			"--user-domains-dir", s.userDomainsDir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "render xray: " + err.Error(), "output": out})
			return
		}
		runCmd("systemctl", "restart", "xray.service")
		if out, err := runCmd("systemctl", "restart", "zapret.service"); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "рестарт zapret: " + err.Error(), "output": out})
			return
		}

		// T-vps-pin: единственный путь смены режима на/с "vps" — что через
		// этот bulk-сейв (общий редактор, кнопки zapret/vps/direct/auto), что
		// раньше был отдельный переключатель в VPSDomainsPanel (убран — два
		// разных контрола на одно и то же поле только путали, см. живой
		// разговор 2026-08-16). Здесь и мгновенная чистка старых DPI-групп
		// (переход НА vps), и постановка на перепроверку (переход С vps) —
		// с прогресс-баром через уже существующий /api/services/{id}/pin-vps
		// GET (см. pinvps.go).
		var pinJobs []string
		for _, v := range svc {
			was, becomes := oldMode[v.ID], v.Mode
			if was == becomes {
				continue
			}
			// tryClaimPinVPSJob — СИНХРОННО, до go: живой баг (2026-08-16) —
			// быстрый двойной toggle одного сервиса запускал две горутины
			// почти одновременно, обе писали в один pinVPSJobs.m[id] и обе же
			// удаляли его по завершении — какая закончилась раньше, стирала
			// прогресс ещё работающей другой. Если слот уже занят (job для
			// этого сервиса ещё идёт) — просто не стартуем вторую, первая
			// доведёт до конца сама.
			if becomes == "vps" {
				if tryClaimPinVPSJob(v.ID) {
					go s.runPinVPSCleanup(v.ID, v.Domains)
					pinJobs = append(pinJobs, v.ID)
				}
			} else if was == "vps" {
				if tryClaimPinVPSJob(v.ID) {
					go s.runVPSPinRecheck(v.ID, v.Domains)
					pinJobs = append(pinJobs, v.ID)
				}
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pinJobs": pinJobs})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStrategies — GET каталог готовых стратегий (пресеты из flowseal),
// встроен в бинарь. UI показывает их в панели и вставляет в канал сервиса.
func (s *server) handleStrategies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(strategiesJSON)
}

func validateServices(svc []zService) string {
	seen := map[string]bool{}
	for _, s := range svc {
		if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Name) == "" {
			return "у сервиса пустой id или имя"
		}
		if seen[s.ID] {
			return "повторяющийся id: " + s.ID
		}
		seen[s.ID] = true
		if len(s.Channels) == 0 {
			return s.Name + ": нет каналов (tcp/udp)"
		}
		for _, c := range s.Channels {
			if c.Proto != "tcp" && c.Proto != "udp" {
				return s.Name + ": proto должен быть tcp или udp"
			}
			if strings.TrimSpace(c.Ports) == "" {
				return s.Name + ": пустые порты"
			}
			if !strings.Contains(c.Desync, "--dpi-desync") {
				return s.Name + ": стратегия без --dpi-desync"
			}
		}
	}
	return ""
}

const zupdateUnit = "gateway-zupdate"

// handleZapretVersion — GET текущий коммит /opt/zapret + статус обновления.
func (s *server) handleZapretVersion(w http.ResponseWriter, r *http.Request) {
	zb := filepath.Dir(s.blockcheck)
	commit, _ := runCmd("git", "-C", zb, "rev-parse", "--short", "HEAD")
	desc, _ := runCmd("git", "-C", zb, "log", "-1", "--format=%cs %s", "HEAD")
	updating := false
	if out, _ := runCmd("systemctl", "is-active", zupdateUnit); strings.TrimSpace(out) == "active" {
		updating = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"commit":   strings.TrimSpace(commit),
		"desc":     strings.TrimSpace(desc),
		"updating": updating,
		"log_tail": tailFile(s.zupdateLog(), 40),
	})
}

func (s *server) zupdateLog() string { return filepath.Join(filepath.Dir(s.scanDir), "zupdate.log") }

// handleZapretUpdate — POST: git fetch+reset апстрима + пересборка + рестарт zapret, в фоне.
func (s *server) handleZapretUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if out, _ := runCmd("systemctl", "is-active", zupdateUnit); strings.TrimSpace(out) == "active" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "обновление уже идёт"})
		return
	}
	zb := filepath.Dir(s.blockcheck)
	sh := "#!/bin/bash\n" +
		"exec > " + shq(s.zupdateLog()) + " 2>&1\n" +
		"set -x\n" +
		"cd " + shq(zb) + " || exit 1\n" +
		"before=$(git rev-parse --short HEAD)\n" +
		"git fetch --depth 1 origin && git reset --hard FETCH_HEAD || exit 1\n" +
		"after=$(git rev-parse --short HEAD)\n" +
		"make -C nfq && make -C mdig && make -C tpws\n" +
		"systemctl restart zapret.service\n" +
		// Mission Timeline (T-shattl-gwui-feedback, 2026-08-06) — та же
		// запись, что и у еженедельного таймера (scripts/zapret-auto-update.sh).
		"python3 -c \"\n" +
		"import json, datetime\n" +
		"line = json.dumps({'at': datetime.datetime.utcnow().isoformat()+'Z', 'kind': 'engine.updated', 'message': 'zapret: $before -> $after'})\n" +
		"open('/etc/gateway/timeline.jsonl', 'a').write(line + chr(10))\n" +
		"\" 2>/dev/null || true\n" +
		"echo __ZUPDATE_DONE__ rc=$?\n"
	scriptPath := filepath.Join(filepath.Dir(s.scanDir), "zupdate.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := os.WriteFile(scriptPath, []byte(sh), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	runCmd("systemctl", "reset-failed", zupdateUnit)
	if out, err := runCmd("systemd-run", "--unit="+zupdateUnit, "--collect", "/bin/bash", scriptPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "systemd-run: " + err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleZapret(w http.ResponseWriter, r *http.Request) {
	out, _ := runCmd("bash", "-c", "pgrep -a nfqws")
	if strings.TrimSpace(out) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "strategies": []zStrategy{}})
		return
	}
	// featured-сервисы (youtube/discord/instagram) показываются в своих вкладках —
	// в общем списке Zapret их сегменты не показываем.
	featured := map[string]bool{}
	featuredL7 := map[string]bool{}
	svcs, _ := s.readServices()
	for _, sv := range svcs {
		if sv.Featured {
			featured[sv.ID] = true
			for _, c := range sv.Channels {
				if c.L7 != "" {
					featuredL7[c.L7] = true
				}
			}
		}
	}
	var strategies []zStrategy
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// fields[0] = PID, дальше команда; найдём общий --qnum
		queue := flagVal(fields, "--qnum")
		// разбить по --new на сегменты-стратегии
		for _, seg := range splitByNew(fields) {
			st := zStrategy{Queue: queue}
			if v := flagVal(seg, "--filter-tcp"); v != "" {
				st.Proto, st.Ports = "tcp", v
			} else if v := flagVal(seg, "--filter-udp"); v != "" {
				st.Proto, st.Ports = "udp", v
			} else {
				continue // не сегмент стратегии (напр. начало с --daemon без фильтра)
			}
			st.L7 = flagVal(seg, "--filter-l7")
			if st.L7 != "" && featuredL7[st.L7] {
				continue // l7-сегмент featured-сервиса (напр. discord voice)
			}
			st.Desync = flagVal(seg, "--dpi-desync")
			st.Repeats = flagVal(seg, "--dpi-desync-repeats")
			st.Fooling = flagVal(seg, "--dpi-desync-fooling")
			if hl := flagVal(seg, "--hostlist"); hl != "" {
				if featured[strings.TrimSuffix(baseName(hl), ".txt")] {
					continue // featured — в своей вкладке
				}
				st.List = baseName(hl)
				st.Domains = readLines(hl)
			}
			strategies = append(strategies, st)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": true, "strategies": strategies})
}

// flagVal ищет токен вида --key=value и возвращает value.
func flagVal(tokens []string, key string) string {
	p := key + "="
	for _, t := range tokens {
		if strings.HasPrefix(t, p) {
			return strings.TrimPrefix(t, p)
		}
	}
	return ""
}

// splitByNew режет список токенов на сегменты по разделителю --new.
func splitByNew(tokens []string) [][]string {
	var segs [][]string
	cur := []string{}
	for _, t := range tokens {
		if t == "--new" {
			segs = append(segs, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, t)
	}
	segs = append(segs, cur)
	return segs
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}
