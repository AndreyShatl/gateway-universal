package main

import (
	"bufio"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// pinvps.go (T-vps-pin, 2026-08-16) — фоновая работа при переключении режима
// сервиса (discord/youtube/instagram и любого другого) на/с "vps": чистка
// существующих DPI-групп при переходе НА vps, постановка на перепроверку при
// уходе С vps. Само переключение живёт в общем редакторе сервисов
// (zapret.go, handleServices POST /api/zapret/services, кнопки zapret/vps/
// direct/auto) — здесь только background job + прогресс через GET
// /api/services/{id}/pin-vps.
//
// Раньше (T-vps-pin, первая версия) был отдельный переключатель прямо в
// VPSDomainsPanel — свой мгновенный POST-эндпоинт, в обход общего редактора.
// Два разных контрола на одно и то же поле `mode` только путали (живой
// разговор с пользователем 2026-08-16) — свели в один путь, здесь остался
// только механизм фоновой работы.
//
// Живой кейс, из-за которого это появилось: пользователь попросил "перевести
// весь Discord на VPS ради эксперимента" — 41 домен, каждый снимался вручную
// через brain-apply.sh (медленно — общая ciadpi-группа на сотни доменов
// частично пересобирается на каждое снятие), и следующий же ночной проход
// расползся бы обратно на ciadpi без правки brain-worker.sh (см. T-vps-pin
// там же).

var pinVPSJobs = struct {
	sync.Mutex
	m map[string]*pinVPSJob
}{m: map[string]*pinVPSJob{}}

type pinVPSJob struct {
	Total int    `json:"total"`
	Done  int    `json:"done"`
	Error string `json:"error,omitempty"`
}

// tryClaimPinVPSJob — атомарно занимает слот под id сервиса, ПЕРЕД тем как
// запускать фоновую горутину (не внутри неё — иначе два быстрых toggle
// подряд оба проходят проверку до того, как первый успеет что-то записать).
// Живой баг (2026-08-16): пользователь быстро переключил VPS -> zapret,
// обе фоновые задачи (чистка от первого клика + перепроверка от второго)
// писали в ОДНУ ячейку pinVPSJobs.m[id] и обе её же удаляли по завершении —
// какая закончилась раньше, стирала прогресс ещё работающей другой,
// прогресс-бар на фронте пропадал, хотя работа продолжалась. Теперь второй
// запуск для уже занятого id просто не стартует (лог-предупреждение,
// первая задача доводится до конца сама).
func tryClaimPinVPSJob(id string) bool {
	pinVPSJobs.Lock()
	defer pinVPSJobs.Unlock()
	if _, busy := pinVPSJobs.m[id]; busy {
		return false
	}
	pinVPSJobs.m[id] = &pinVPSJob{} // место занято, реальный Total выставит сама job чуть позже
	return true
}

// handlePinVPS — только опрос прогресса фоновой job'ы (GET). Само
// переключение режима — через общий редактор, см. комментарий выше.
func (s *server) handlePinVPS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "нет id сервиса"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET"})
		return
	}
	pinVPSJobs.Lock()
	job := pinVPSJobs.m[id]
	pinVPSJobs.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

// runPinVPSCleanup — снимает существующие DPI-группы с каждого домена сервиса
// (brain-apply.sh vps уже включает remove для всех трёх движков) в фоне,
// не блокируя ответ пользователю. Прогресс — через /api/services/{id}/pin-vps
// GET (пока job жив). Вызывающий (zapret.go) уже занял слот через
// tryClaimPinVPSJob ДО запуска этой горутины — здесь только дописываем
// реальный Total в уже существующий объект (не создаём новый — иначе снова
// гонка, см. tryClaimPinVPSJob).
func (s *server) runPinVPSCleanup(id string, domains []string) {
	pinVPSJobs.Lock()
	job := pinVPSJobs.m[id]
	job.Total = len(domains)
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

const brainQueueFile = "/etc/gateway/brain-queue"
const brainQueueLockFile = "/etc/gateway/brain-queue.lock"
const vpsPinRecheckMaxWait = 2 * time.Hour

// runVPSPinRecheck — ставит все домены сервиса в ту же очередь brain-queue,
// что использует пассивный детектор и ночная переоценка (одна точка
// правды, никакого отдельного механизма) и отслеживает прогресс, пока
// brain-worker.sh (обычный, последовательный, ничего параллельно не
// форсируем — "не мешать другому" обеспечивается именно тем, что мы просто
// докидываем в СУЩЕСТВУЮЩУЮ очередь, а не создаём конкурирующий процесс)
// их не разберёт. Прогресс — тот же /api/services/{id}/pin-vps GET и
// pinVPSJobs, что и у "закрепить на VPS".
func (s *server) runVPSPinRecheck(id string, domains []string) {
	enqueued := enqueueDomainsForRecheck(domains)
	if len(enqueued) == 0 {
		// все уже были в очереди — нечего отслеживать, но слот, занятый
		// tryClaimPinVPSJob, всё равно нужно освободить, иначе он навсегда
		// заблокирует любой следующий toggle этого сервиса (место "занято"
		// пустышкой, которую больше некому удалить).
		pinVPSJobs.Lock()
		delete(pinVPSJobs.m, id)
		pinVPSJobs.Unlock()
		return
	}

	pinVPSJobs.Lock()
	job := pinVPSJobs.m[id]
	job.Total = len(enqueued)
	pinVPSJobs.Unlock()

	pending := make(map[string]bool, len(enqueued))
	for _, d := range enqueued {
		pending[d] = true
	}

	deadline := time.Now().Add(vpsPinRecheckMaxWait)
	for len(pending) > 0 && time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		stillQueued := readQueueDomainSet()
		for d := range pending {
			if !stillQueued[d] {
				delete(pending, d)
				pinVPSJobs.Lock()
				job.Done++
				pinVPSJobs.Unlock()
			}
		}
	}

	s.timeline.Record("service.pin-vps", id+": перепроверка завершена ("+strconv.Itoa(len(enqueued))+" доменов поставлено в очередь)")

	pinVPSJobs.Lock()
	delete(pinVPSJobs.m, id)
	pinVPSJobs.Unlock()
}

// enqueueDomainsForRecheck — та же дедуп-механика (flock + проверка "уже в
// очереди"), что enqueueBrain в detector/main.go, просто со стороны
// gateway-ui и сразу для целого списка доменов. Возвращает те, что реально
// добавлены (уже стоявшие в очереди не отслеживаем повторно).
func enqueueDomainsForRecheck(domains []string) []string {
	lf, err := os.OpenFile(brainQueueLockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}
	defer lf.Close()
	syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	existing := readQueueDomainSetLocked()
	f, err := os.OpenFile(brainQueueFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	defer f.Close()

	var added []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || existing[d] {
			continue
		}
		if _, err := f.WriteString(d + "\treeval\n"); err == nil {
			added = append(added, d)
			existing[d] = true
		}
	}
	return added
}

func readQueueDomainSet() map[string]bool {
	lf, err := os.OpenFile(brainQueueLockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return readQueueDomainSetLocked()
	}
	defer lf.Close()
	syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	return readQueueDomainSetLocked()
}

func readQueueDomainSetLocked() map[string]bool {
	set := map[string]bool{}
	f, err := os.Open(brainQueueFile)
	if err != nil {
		return set
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		domain := strings.ToLower(strings.SplitN(line, "\t", 2)[0])
		set[domain] = true
	}
	return set
}
