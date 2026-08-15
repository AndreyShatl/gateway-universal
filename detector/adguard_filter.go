package main

// adguard_filter.go (T-junk-filter, 2026-08-16) — фильтрация мусорных доменов
// (реклама/трекеры/фишинг-типосквоттинг) через уже загруженные списки
// AdGuardHome, вместо ручного пополнения gwdb whitelist по одному домену.
//
// НЕ используем gwdb.py whitelist для этого: cmd_whitelisted там — линейный
// перебор в Python по ВСЕЙ таблице на каждый вызов (см. scripts/gwdb.py) —
// годится для десятков ручных записей, но при ~166k строк из блок-листов
// убьёт латентность живого детектора трафика. Поэтому свой in-memory
// hash-set, загружается из уже скачанных AdGuardHome файлов
// (/opt/AdGuardHome/data/filters/*.txt — те же, что использует сам AGH для
// DNS-фильтрации, обновляются им самим по расписанию) — отдельный источник
// правды, не дублирование данных вручную, всегда синхронно с тем, что уже
// блокируется на DNS-уровне.
//
// Формат AdBlock hostlist: подавляющее большинство строк — "||domain.tld^"
// (блок), небольшая часть — "@@||domain.tld^" (исключение/allow, приоритет
// выше блока). Комментарии начинаются с "!" или "#", остальные экзотические
// правила (regex/^$important и т.п.) пропускаем — не наш случай, лучше
// пропустить редкое правило, чем неверно распарсить и словить false-positive
// на легитимном домене.

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// T-junk-filter (2026-08-16): AdGuardHome сам назначает id фильтру при
// добавлении через API (/control/filtering/add_url) — на новой установке
// (install.sh) он НЕ обязан совпасть с тем, что было прописано вручную на
// 132/Pi при разработке этой фичи. Поэтому вместо жёстко заданных id —
// просто ВСЕ *.txt в директории кеша AdGuardHome: на управляемой install.sh
// установке там ровно те 4 списка, что мы сами и зарегистрировали (реклама/
// трекеры/DoH-обход/фишинг-тайпсквоттинг, включая HaGeZi Threat
// Intelligence Feed, ~2 млн строк) — не более. Живой замер (memtest,
// отдельная программа с той же логикой парсинга) показал ~156МБ на весь
// набор, проверено на 132 (1.9ГБ RAM) без деградации. ADGUARD_FILTER_FILES
// (env, через systemd override) — аварийный рубильник, сузить список БЕЗ
// пересборки: `systemctl edit gateway-detector` -> Environment= с нужным
// подмножеством путей -> restart.
const adguardFilterDir = "/opt/AdGuardHome/data/filters"

var adguardFilterFiles = func() []string {
	if v := os.Getenv("ADGUARD_FILTER_FILES"); v != "" {
		return strings.Split(v, ",")
	}
	matches, err := filepath.Glob(filepath.Join(adguardFilterDir, "*.txt"))
	if err != nil || len(matches) == 0 {
		log.Printf("adguard-filter: нет файлов в %s (%v) — junk-фильтр отключён", adguardFilterDir, err)
	}
	return matches
}()

const adguardReloadInterval = 6 * time.Hour

var adguardBlockSet atomic.Value // map[string]struct{}
var adguardAllowSet atomic.Value // map[string]struct{}
var adguardLoadOnce sync.Once

func parseAdGuardLine(line string) (domain string, allow bool, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
		return "", false, false
	}
	allow = strings.HasPrefix(line, "@@")
	if allow {
		line = line[2:]
	}
	if !strings.HasPrefix(line, "||") {
		return "", false, false // не доменное правило (regex/путь/etc.) — пропускаем
	}
	line = line[2:]
	// срезаем модификаторы после ^ или $ (типы трафика, важность и т.п.)
	if i := strings.IndexAny(line, "^$/"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimPrefix(line, "*.")
	line = strings.ToLower(strings.TrimSuffix(line, "."))
	if line == "" || strings.ContainsAny(line, "*/ ") {
		return "", false, false // остаточный wildcard/мусор — пропускаем, не наш формат
	}
	return line, allow, true
}

func loadAdGuardSets() (map[string]struct{}, map[string]struct{}) {
	block := make(map[string]struct{}, 170000)
	allow := make(map[string]struct{}, 1000)
	loaded := 0
	for _, path := range adguardFilterFiles {
		f, err := os.Open(path)
		if err != nil {
			log.Printf("adguard-filter: не открыть %s: %v (пропуск)", path, err)
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			d, isAllow, ok := parseAdGuardLine(sc.Text())
			if !ok {
				continue
			}
			if isAllow {
				allow[d] = struct{}{}
			} else {
				block[d] = struct{}{}
			}
			loaded++
		}
		f.Close()
	}
	log.Printf("adguard-filter: загружено %d правил (%d блок/%d allow) из %d файлов", loaded, len(block), len(allow), len(adguardFilterFiles))
	return block, allow
}

// isAdGuardBlocked — домен или любой его родительский суффикс есть в блок-
// списке AdGuardHome, И не переопределён более специфичным allow-правилом.
// Суффиксная проверка (не только точное совпадение) — блок-листы обычно
// покрывают базовый домен трекера/спамера, а реальный SNI часто на
// поддомене (напр. "track.adnetwork.com" при правиле "||adnetwork.com^").
func isAdGuardBlocked(domain string) bool {
	// ленивый старт (не привязываемся к конкретной точке входа — их две,
	// watch/watch-ebpf, обе используют buildCandidateHandler): первый вызов
	// синхронно грузит списки (разовая задержка на первом кандидате,
	// незаметно на фоне pcap/eBPF), дальше — фоновый реload по таймеру.
	adguardLoadOnce.Do(func() {
		block, allow := loadAdGuardSets()
		adguardBlockSet.Store(block)
		adguardAllowSet.Store(allow)
		go func() {
			for {
				time.Sleep(adguardReloadInterval)
				b, a := loadAdGuardSets()
				adguardBlockSet.Store(b)
				adguardAllowSet.Store(a)
			}
		}()
	})
	bv := adguardBlockSet.Load()
	if bv == nil {
		return false // не удалось загрузить ни один файл — fail-open, не блокируем анализ
	}
	block := bv.(map[string]struct{})
	av, _ := adguardAllowSet.Load().(map[string]struct{})

	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	labels := strings.Split(domain, ".")
	for i := 0; i < len(labels)-1; i++ { // -1: не проверяем голый TLD
		suffix := strings.Join(labels[i:], ".")
		if av != nil {
			if _, ok := av[suffix]; ok {
				return false // явный allow перевешивает блок
			}
		}
		if _, ok := block[suffix]; ok {
			return true
		}
	}
	return false
}
