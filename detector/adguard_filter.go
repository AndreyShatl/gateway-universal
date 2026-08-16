package main

// adguard_filter.go (T-junk-filter, 2026-08-16; T-adguard-api, 2026-08-16
// вечер) — фильтрация мусорных доменов (реклама/трекеры/фишинг-
// типосквоттинг) через уже настроенные блок-листы AdGuardHome.
//
// Изначально (T-junk-filter) детектор держал СВОЮ копию всех блок-листов в
// памяти (in-memory hash-set, ~165k-2.2М записей в зависимости от набора) —
// работало, но дублировало то, что и так уже полностью загружено в память
// самим AdGuardHome (у него те же списки нужны для DNS-фильтрации, это его
// основная функция, не опционально). На слабом 132 (1.9ГБ RAM) с HaGeZi TIF
// (2 млн записей) AdGuardHome сам держит ~870МБ, детектор поверх этого ещё
// ~350МБ на дублирующую копию — почти 1.2ГБ из 1.9ГБ суммарно на ОДНУ и ту
// же информацию в двух процессах.
//
// Теперь — прямой запрос к API AdGuardHome (/control/filtering/check_host,
// тот же эндпоинт, что использует его собственный веб-интерфейс) вместо
// собственной копии списков. AdGuardHome остаётся единственным источником
// правды в памяти; детектор просто спрашивает. Локальный кеш ответов (TTL
// 10 мин) — не бить по API на каждый повторный кандидат в короткий срок,
// но и не держать ничего похожего на полную копию списков.
import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// adguardPassword — пароль admin AdGuardHome (Basic Auth для API). Задаётся
// явно в runWatch()/runWatchEBPF() через configVar(configEnv, "ADGUARD_PASSWORD")
// (тот же паттерн, что gwdbScript в main.go/ebpf_on.go) — здесь только дефолт
// для случая, когда её не задали (например, запуск detector напрямую в CLI).
var adguardPassword = ""

const adguardAPIBase = "http://127.0.0.1:3000"
const adguardCacheTTL = 10 * time.Minute

type adguardCacheEntry struct {
	blocked bool
	expiry  time.Time
}

var adguardCache = struct {
	sync.Mutex
	m map[string]adguardCacheEntry
}{m: map[string]adguardCacheEntry{}}

var adguardHTTPClient = &http.Client{Timeout: 3 * time.Second}

type adguardCheckHostResp struct {
	Reason string `json:"reason"`
}

// isAdGuardBlocked — спрашивает AdGuardHome напрямую (его /control/filtering/
// check_host, тот же путь, что использует собственный веб-интерфейс AGH при
// показе "почему заблокировано"). Fail-open при любой ошибке (AGH недоступен,
// таймаут, неожиданный ответ) — не блокируем анализ трафика из-за сбоя
// вспомогательного запроса, максимум пропустим фильтрацию мусора разово.
func isAdGuardBlocked(domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return false
	}

	adguardCache.Lock()
	if e, ok := adguardCache.m[domain]; ok && time.Now().Before(e.expiry) {
		blocked := e.blocked
		adguardCache.Unlock()
		return blocked
	}
	adguardCache.Unlock()

	blocked := queryAdGuardHome(domain)

	adguardCache.Lock()
	adguardCache.m[domain] = adguardCacheEntry{blocked: blocked, expiry: time.Now().Add(adguardCacheTTL)}
	adguardCache.Unlock()

	return blocked
}

var adguardPasswordWarnOnce sync.Once

func queryAdGuardHome(domain string) bool {
	if adguardPassword == "" {
		adguardPasswordWarnOnce.Do(func() {
			log.Printf("adguard-filter: пароль AdGuardHome не найден в config.env — junk-фильтр отключён (fail-open)")
		})
		return false // пароль не настроен/не найден — fail-open, не блокируем анализ
	}

	req, err := http.NewRequest("GET", adguardAPIBase+"/control/filtering/check_host?name="+domain, nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth("admin", adguardPassword)

	resp, err := adguardHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	var parsed adguardCheckHostResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return false
	}
	return strings.HasPrefix(parsed.Reason, "Filtered")
}
