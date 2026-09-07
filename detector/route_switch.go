package main

// route_switch.go (Этап 5 / STAGE 6 TEST DESTINATIONS из схемы Shattl Brain,
// 2026-09-07) — транзакция переключения маршрута домена по разделу 21 схемы:
//
//	VALIDATE → APPLY → CONNECTIVITY CHECK → COMMIT
//	(при провале любого шага — ROLLBACK и восстановление прежнего маршрута)
//
// Схема раздела 31: Active Mode на проде без явного разрешения нельзя —
// поэтому ДЕФОЛТ dry-run: без --commit выполняется только VALIDATE и печать
// плана (что БЫ сделали на каждом шаге). Реальные переключения — только
// флагом --commit, по одному домену за раз.
//
// Построено на существующих примитивах (ничего нового в исполнении):
//   - brain-apply.sh vps|zapret|ciadpi|zapret2 <domain> … — единственный,
//     кто трогает iptables/ipset/демонов (тот же, что UI и детектор);
//   - prober.Probe — та же проверка, что использует детектор;
//   - снапшот route-state — источник прежнего состояния для ROLLBACK.
// Все транзакции пишутся в observe/transactions.jsonl (аудит STAGE 6).

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gateway-detector/prober"
)

const (
	brainApplyBin = "/opt/gateway-brain/brain-apply.sh"
	transactionsF = observeStateDir + "/transactions.jsonl"
	vpsSocksAddr  = "127.0.0.1:1081"
)

type TxStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Took   string `json:"took,omitempty"`
}

type TxRecord struct {
	TS       string   `json:"ts"`
	Domain   string   `json:"domain"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Commit   bool     `json:"commit"` // false = dry-run
	Steps    []TxStep `json:"steps"`
	Outcome  string   `json:"outcome"` // dry_run_plan|committed|rolled_back|validation_failed|rollback_failed
	Rollback string   `json:"rollback,omitempty"`
}

func (tx *TxRecord) step(name string, ok bool, detail string) {
	tx.Steps = append(tx.Steps, TxStep{Name: name, OK: ok, Detail: detail, Took: ""})
	fmt.Printf("  %-4s %s — %s\n", map[bool]string{true: "✓", false: "✗"}[ok], name, detail)
}

// findDest — свежее состояние домена из лёгкого снапшота.
func findDest(domain string) (*DestinationState, *RouteSnapshot) {
	snap, _ := buildRouteSnapshot(false)
	for i := range snap.Destinations {
		if snap.Destinations[i].Domain == domain {
			return &snap.Destinations[i], snap
		}
	}
	return nil, snap
}

// probeVPS / probeDirect — та же семантика, что у детектора в бою.
func probeVPS(domain string) (bool, string) {
	res := prober.Probe(domain, 443, domain, prober.Config{SocksAddr: vpsSocksAddr, Timeout: 8 * time.Second, TLS: true})
	return res.Verdict == prober.OK, fmt.Sprintf("через VPS-socks: verdict=%s via_vps=%s", res.Verdict, res.ViaVPS)
}

func probeDirect(domain string) (bool, string) {
	res := prober.Probe(domain, 443, domain, prober.Config{Timeout: 8 * time.Second, TLS: true})
	return res.Verdict == prober.OK, fmt.Sprintf("напрямую с DPI-стратегией: verdict=%s direct=%s", res.Verdict, res.Direct)
}

func brainApply(args ...string) (bool, string) {
	cmd := exec.Command("bash", append([]string{brainApplyBin}, args...)...)
	out, err := cmd.CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if err != nil {
		detail = fmt.Sprintf("exit=%v: %s", err, detail)
	}
	return err == nil, detail
}

// runRouteSwitch — gateway-detector route-switch <domain> --to vps|local
//
//	[--engine zapret|ciadpi|zapret2 --strategy "<args>"] [--commit]
func runRouteSwitch() {
	fs := flag.NewFlagSet("route-switch", flag.ExitOnError)
	to := fs.String("to", "", "vps|local")
	engine := fs.String("engine", "", "для --to local: zapret|ciadpi|zapret2")
	strategy := fs.String("strategy", "", "для --to local: полные args стратегии (как в brain-services)")
	commit := fs.Bool("commit", false, "РЕАЛЬНО применить (иначе dry-run: только VALIDATE + план)")
	// домен можно писать до или после флагов (go flag не переваривает
	// позиционный аргумент перед флагами — разложим руками)
	var flagsOnly, positional []string
	for _, a := range os.Args[2:] {
		if strings.HasPrefix(a, "-") {
			flagsOnly = append(flagsOnly, a)
		} else {
			positional = append(positional, a)
		}
	}
	fs.Parse(flagsOnly)
	if len(positional) == 0 || *to == "" || (*to != "vps" && *to != "local") {
		fmt.Fprintln(os.Stderr, "usage: gateway-detector route-switch <domain> --to vps|local [--engine E --strategy \"args\"] [--commit]")
		os.Exit(2)
	}
	domain := strings.ToLower(strings.TrimSpace(positional[0]))
	tx := &TxRecord{TS: time.Now().UTC().Format(time.RFC3339), Domain: domain, To: *to, Commit: *commit}

	fmt.Printf("=== route-switch %s → %s (%s) ===\n", domain, *to, map[bool]string{true: "COMMIT", false: "DRY-RUN"}[*commit])

	// --- VALIDATE ---
	dest, _ := findDest(domain)
	if dest == nil {
		tx.step("validate", false, "домен не найден в снапшоте route-state (не управляется ни одной подсистемой)")
		tx.Outcome = "validation_failed"
		finishTx(tx)
		return
	}
	tx.From = dest.CurrentRoute
	tx.step("validate", true, fmt.Sprintf("текущий маршрут: %s (engine=%s group=%s)", dest.CurrentRoute, dest.Engine, dest.GroupID))

	if *to == "vps" {
		if dest.CurrentRoute == "vps_auto" {
			tx.step("validate", false, "домен уже на VPS — переключать нечего")
			tx.Outcome = "validation_failed"
			finishTx(tx)
			return
		}
		if dest.Pinned {
			tx.step("validate", false, "домен пиннед (ai-services/STATIC) — схемы: никогда не трогать")
			tx.Outcome = "validation_failed"
			finishTx(tx)
			return
		}
	} else { // local
		if strings.HasPrefix(dest.CurrentRoute, "local_") {
			tx.step("validate", false, "домен уже на LOCAL-маршруте")
			tx.Outcome = "validation_failed"
			finishTx(tx)
			return
		}
		if *engine == "" || *strategy == "" {
			tx.step("validate", false, "--to local требует --engine и --strategy (аргументы как в brain-services*.json; источник — route-state/журнал)")
			tx.Outcome = "validation_failed"
			finishTx(tx)
			return
		}
	}

	if !*commit {
		if *to == "vps" {
			tx.step("plan/apply", true, fmt.Sprintf("БЫ: brain-apply.sh vps %s (снимет из DPI-групп %s, поставит в VPS-автообход)", domain, dest.GroupID))
		} else {
			tx.step("plan/apply", true, fmt.Sprintf("БЫ: brain-apply.sh %s %s tcp %s", *engine, domain, *strategy))
		}
		tx.step("plan/check", true, "БЫ: проверка досягаемости домена новым маршрутом (prober)")
		tx.step("plan/commit", true, "БЫ: фиксация, запись в transactions.jsonl")
		tx.Outcome = "dry_run_plan"
		finishTx(tx)
		return
	}

	// --- APPLY (запоминаем прежнее для отката) ---
	prevEngine, prevStrategy := dest.Engine, dest.Strategy
	var applyOK bool
	var applyDetail string
	if *to == "vps" {
		applyOK, applyDetail = brainApply("vps", domain)
	} else {
		applyOK, applyDetail = brainApply(*engine, domain, "tcp", *strategy)
	}
	tx.step("apply", applyOK, applyDetail)
	if !applyOK {
		tx.Outcome = rollbackTx(tx, domain, prevEngine, prevStrategy, *to)
		finishTx(tx)
		return
	}

	// --- CONNECTIVITY CHECK ---
	var ok bool
	var detail string
	if *to == "vps" {
		ok, detail = probeVPS(domain)
	} else {
		ok, detail = probeDirect(domain)
	}
	tx.step("check", ok, detail)

	if !ok {
		tx.Outcome = rollbackTx(tx, domain, prevEngine, prevStrategy, *to)
		finishTx(tx)
		return
	}

	tx.step("commit", true, "новый маршрут подтверждён проверкой — фиксируем")
	tx.Outcome = "committed"
	finishTx(tx)
}

// rollbackTx — вернуть прежний маршрут. to=vps значит были local (вернём
// стратегию), to=local значит были vps (вернём в автообход).
func rollbackTx(tx *TxRecord, domain, prevEngine, prevStrategy, attemptedTo string) string {
	fmt.Println("  ⚠ CHECK провален — ROLLBACK (раздел 21 схемы)")
	var ok bool
	var detail string
	if attemptedTo == "vps" && prevEngine != "" && prevStrategy != "" {
		ok, detail = brainApply(prevEngine, domain, "tcp", prevStrategy)
	} else {
		ok, detail = brainApply("vps", domain)
	}
	tx.Rollback = detail
	if ok {
		// контроль отката
		if dest, _ := findDest(domain); dest != nil {
			tx.step("rollback-check", dest.CurrentRoute != "", fmt.Sprintf("маршрут после отката: %s", dest.CurrentRoute))
			if dest.CurrentRoute != "" {
				return "rolled_back"
			}
		}
	}
	tx.step("rollback-check", false, "откат не подтвердился — ТРЕБУЕТ ВНИМАНИЯ (см. brain-apply restore)")
	return "rollback_failed"
}

func finishTx(tx *TxRecord) {
	os.MkdirAll(observeStateDir, 0o755)
	if b, err := json.Marshal(tx); err == nil {
		if f, err := os.OpenFile(transactionsF, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	fmt.Printf("=== исход: %s (журнал: %s) ===\n", tx.Outcome, transactionsF)
}
