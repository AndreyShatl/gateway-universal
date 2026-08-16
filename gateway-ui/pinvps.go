package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
)

// pinvps.go (T-vps-pin, 2026-08-16) — мгновенное переключение сервиса
// (discord/youtube/instagram) между "авто-DPI" и "закреплён на VPS".
//
// Живой кейс, из-за которого это появилось: пользователь попросил "перевести
// весь Discord на VPS ради эксперимента" — 41 домен, каждый снимался вручную
// через brain-apply.sh (медленно — общая ciadpi-группа на сотни доменов
// частично пересобирается на каждое снятие), и следующий же ночной проход
// расползся бы обратно на ciadpi без правки brain-worker.sh (см. T-vps-pin
// там же). Кнопка делает то же самое, но: (1) отклик мгновенный — mode
// пишется и xray перерендеривается сразу, фактическая чистка существующих
// DPI-групп уходит в фон; (2) закрепление постоянное — brain-worker.sh
// больше не станет переназначать DPI-обход домену закреплённого сервиса.

var pinVPSJobs = struct {
	sync.Mutex
	m map[string]*pinVPSJob
}{m: map[string]*pinVPSJob{}}

type pinVPSJob struct {
	Total int    `json:"total"`
	Done  int    `json:"done"`
	Error string `json:"error,omitempty"`
}

func (s *server) handlePinVPS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нет id сервиса"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		pinVPSJobs.Lock()
		job := pinVPSJobs.m[id]
		pinVPSJobs.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"job": job})
		return

	case http.MethodPost:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "плохой JSON"})
			return
		}

		svc, err := s.readServices()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		idx := -1
		for i := range svc {
			if svc[i].ID == id {
				idx = i
				break
			}
		}
		if idx == -1 {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "сервис не найден"})
			return
		}

		if body.Enabled {
			svc[idx].Mode = "vps"
		} else {
			svc[idx].Mode = ""
		}
		domains := append([]string(nil), svc[idx].Domains...)

		b, _ := json.MarshalIndent(svc, "", "  ")
		tmp := s.servicesFile + ".tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		os.Rename(tmp, s.servicesFile)

		if out, err := s.runScript("xray/render-config.sh",
			"--template", filepath.Join(s.repoDir, "xray", "config.template.json"),
			"--out", s.xrayConfig, "--config", s.configEnv, "--xray", s.xrayBin,
			"--user-domains-dir", s.userDomainsDir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "render xray: " + err.Error(), "output": out})
			return
		}
		runCmd("systemctl", "restart", "xray.service")

		s.timeline.Record("service.pin-vps", id+": "+map[bool]string{true: "закреплён на VPS", false: "снят с закрепления (авто-DPI)"}[body.Enabled])

		if body.Enabled {
			go s.runPinVPSCleanup(id, domains)
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": svc[idx].Mode})
		return

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET|POST"})
	}
}

// runPinVPSCleanup — снимает существующие DPI-группы с каждого домена сервиса
// (brain-apply.sh vps уже включает remove для всех трёх движков) в фоне,
// не блокируя ответ пользователю. Прогресс — через /api/services/{id}/pin-vps
// GET (пока job жив).
func (s *server) runPinVPSCleanup(id string, domains []string) {
	job := &pinVPSJob{Total: len(domains)}
	pinVPSJobs.Lock()
	pinVPSJobs.m[id] = job
	pinVPSJobs.Unlock()

	brainApply := filepath.Join("/opt/gateway-brain", "brain-apply.sh")
	for _, d := range domains {
		exec.Command("bash", brainApply, "vps", d).Run()
		pinVPSJobs.Lock()
		job.Done++
		pinVPSJobs.Unlock()
	}

	s.timeline.Record("service.pin-vps", id+": фоновая чистка завершена ("+strconv.Itoa(len(domains))+" доменов)")

	pinVPSJobs.Lock()
	delete(pinVPSJobs.m, id)
	pinVPSJobs.Unlock()
}
