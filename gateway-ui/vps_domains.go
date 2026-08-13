package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// vpsDomainEntry — п.1/1.1/1.2/1.3 ТЗ (2026-08-13): для анализа нужно видеть
// КАЖДЫЙ домен из курируемых списков (Discord/Instagram/YouTube/остальные) с
// его текущим фактическим маршрутом — VPS (baseline) или уже сбежал на
// zapret/ciadpi/zapret2 (see brain-apply.sh do_zapret: RETURN в nat
// PREROUTING ставит escape ВЫШЕ статического VPS-роутинга). Раньше вкладка
// "Только через VPS" показывала только то, что застряло на VPS — теперь
// показывает ВСЕ домены списка с колонкой route, чтобы было видно прогресс
// (сколько уже ушло на DPI-обход, сколько ещё нет).
type vpsDomainEntry struct {
	Domain     string `json:"domain"`
	Route      string `json:"route"` // "vps" | "dpi"
	Engine     string `json:"engine,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	LastActive string `json:"last_active,omitempty"`
}

type vpsDomainsResponse struct {
	Discord   []vpsDomainEntry `json:"discord"`
	Instagram []vpsDomainEntry `json:"instagram"`
	Youtube   []vpsDomainEntry `json:"youtube"`
	Other     []vpsDomainEntry `json:"other"`
}

type domainRoute struct {
	engine, groupID, lastActive string
}

// buildDomainRouteIndex — domain -> {engine, group_id, last_active} по всем
// трём brain-состояниям (zapret/ciadpi/zapret2). Отсутствие в индексе значит
// "домен ещё на статическом VPS-роутинге" (baseline, никогда явно не
// записывается отдельным файлом — это то, что остаётся, когда brain ничего
// не сделал).
func buildDomainRouteIndex() map[string]domainRoute {
	idx := map[string]domainRoute{}
	for _, g := range readBrainGroups() {
		for _, d := range g.Domains {
			idx[d] = domainRoute{engine: "zapret", groupID: g.GroupID, lastActive: g.LastActive}
		}
	}
	for _, g := range readCiadpiGroups() {
		for _, d := range g.Domains {
			idx[d] = domainRoute{engine: "ciadpi", groupID: g.GroupID, lastActive: g.LastActive}
		}
	}
	for _, g := range readZapret2Groups() {
		for _, d := range g.Domains {
			idx[d] = domainRoute{engine: "zapret2", groupID: g.GroupID, lastActive: g.LastActive}
		}
	}
	return idx
}

func entriesFor(domains []string, idx map[string]domainRoute) []vpsDomainEntry {
	out := make([]vpsDomainEntry, 0, len(domains))
	seen := map[string]bool{}
	for _, d := range domains {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if r, ok := idx[d]; ok {
			out = append(out, vpsDomainEntry{Domain: d, Route: "dpi", Engine: r.engine, GroupID: r.groupID, LastActive: r.lastActive})
		} else {
			out = append(out, vpsDomainEntry{Domain: d, Route: "vps"})
		}
	}
	return out
}

func readServiceDomains(servicesFile, id string) []string {
	data, err := os.ReadFile(servicesFile)
	if err != nil {
		return nil
	}
	var raw []struct {
		ID      string   `json:"id"`
		Domains []string `json:"domains"`
	}
	json.Unmarshal(data, &raw)
	for _, s := range raw {
		if s.ID == id {
			return s.Domains
		}
	}
	return nil
}

// otherCuratedDomains — xray/domains/*.txt КРОМЕ ai-services.txt (никогда не
// идут в обход, см. brain-static-reeval.sh EXCLUDED_CATEGORIES) и КРОМЕ
// discord.txt/instagram-meta.txt/youtube-google.txt — те уже показаны в
// своих featured-вкладках, дублировать их тут незачем.
func otherCuratedDomains(repoDir string) []string {
	dir := filepath.Join(repoDir, "xray", "domains")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	skip := map[string]bool{"ai-services.txt": true, "discord.txt": true, "instagram-meta.txt": true, "youtube-google.txt": true}
	var out []string
	for _, e := range entries {
		if e.IsDir() || skip[e.Name()] || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
		f.Close()
	}
	return out
}

func (s *server) handleVPSDomains(w http.ResponseWriter, r *http.Request) {
	idx := buildDomainRouteIndex()
	resp := vpsDomainsResponse{
		Discord:   entriesFor(readServiceDomains(s.servicesFile, "discord"), idx),
		Instagram: entriesFor(readServiceDomains(s.servicesFile, "instagram"), idx),
		Youtube:   entriesFor(readServiceDomains(s.servicesFile, "youtube"), idx),
		Other:     entriesFor(otherCuratedDomains(s.repoDir), idx),
	}
	writeJSON(w, http.StatusOK, resp)
}
