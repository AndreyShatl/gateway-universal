// hostmetrics.go — системные метрики самого шлюза (CPU/RAM/Swap/Disk/Temp/
// Load) для Overview-панели ТЗ Shattl Gateway (2026-08-05). Независимая копия
// того же алгоритма, что и gmp-agent/internal/collector (тот же автор/репо,
// не сторонний код) — сознательно не общий пакет между репозиториями: цель
// локальной панели — работать даже если gmp-agent не установлен/не запущен
// на этой машине, дублирование дешевле новой межпроцессной зависимости.
package main

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type hostMetrics struct {
	UptimeS     int64     `json:"uptime_s"`
	CPUPct      float64   `json:"cpu_pct"`
	MemoryPct   float64   `json:"memory_pct"`
	MemTotalMB  float64   `json:"mem_total_mb"`
	SwapPct     float64   `json:"swap_pct"`
	SwapTotalMB float64   `json:"swap_total_mb"`
	DiskPct     float64   `json:"disk_pct"`
	DiskTotalGB float64   `json:"disk_total_gb"`
	CPUTempC    float64   `json:"cpu_temp_c"`
	LoadAvg1    float64   `json:"load_avg_1"`
	LoadAvg5    float64   `json:"load_avg_5"`
	LoadAvg15   float64   `json:"load_avg_15"`
	CPUCores    int       `json:"cpu_cores"`
	CPUMHz      float64   `json:"cpu_mhz"`
	PerCorePct  []float64 `json:"per_core_pct"`
}

func collectHostMetrics() hostMetrics {
	swapPct, swapTotalMB := swapUsedPct()
	load1, load5, load15 := loadAvg()
	cores, mhz := cpuInfo()
	cpuPct, perCore := cpuPctSample()
	return hostMetrics{
		UptimeS:     uptimeSeconds(),
		CPUPct:      cpuPct,
		PerCorePct:  perCore,
		MemoryPct:   memoryUsedPct(),
		MemTotalMB:  memTotalMB(),
		SwapPct:     swapPct,
		SwapTotalMB: swapTotalMB,
		DiskPct:     diskUsedPct("/"),
		DiskTotalGB: diskTotalGB("/"),
		CPUTempC:    cpuTempC(),
		LoadAvg1:    load1,
		LoadAvg5:    load5,
		LoadAvg15:   load15,
		CPUCores:    cores,
		CPUMHz:      mhz,
	}
}

func (s *server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, collectHostMetrics())
}

// cpuSampleDelay — /proc/stat хранит накопительные счётчики тиков, не
// мгновенную загрузку — нужны два снимка с паузой между ними.
const cpuSampleDelay = 200 * time.Millisecond

// cpuPctSample — общий CPU% + по-ядерный срез (для полосок в стиле htop,
// ТЗ 2026-08-06). Одна пара снимков /proc/stat на оба результата — нет
// смысла спать 200мс дважды ради того же файла.
func cpuPctSample() (total float64, perCore []float64) {
	first, err := readAllCPUStat()
	if err != nil {
		return 0, nil
	}
	time.Sleep(cpuSampleDelay)
	second, err := readAllCPUStat()
	if err != nil {
		return 0, nil
	}
	if len(first) == 0 || len(second) == 0 {
		return 0, nil
	}
	total = cpuPctFromSamples(first[0], second[0])
	perCore = make([]float64, 0, len(second)-1)
	for i := 1; i < len(second) && i < len(first); i++ {
		perCore = append(perCore, cpuPctFromSamples(first[i], second[i]))
	}
	return total, perCore
}

type cpuStat struct {
	idle  uint64
	total uint64
}

// readAllCPUStat — [0]=агрегат ("cpu "), [1:]=по ядрам ("cpu0".."cpuN") —
// именно в порядке строк /proc/stat, который совпадает с "processor" в
// /proc/cpuinfo (cpuInfo()) на всех обычных Linux-системах.
func readAllCPUStat() ([]cpuStat, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	var out []cpuStat
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			break // строки cpuN идут подряд в начале файла, дальше — intr/ctxt/...
		}
		st, err := parseCPUStatLine(line)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, strconv.ErrSyntax
	}
	return out, nil
}

func parseCPUStatLine(line string) (cpuStat, error) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return cpuStat{}, strconv.ErrSyntax
	}
	var total, idle uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return cpuStat{}, err
		}
		total += v
		if i == 3 { // idle
			idle = v
		}
	}
	return cpuStat{idle: idle, total: total}, nil
}

func cpuPctFromSamples(first, second cpuStat) float64 {
	totalDelta := second.total - first.total
	if totalDelta == 0 {
		return 0
	}
	idleDelta := second.idle - first.idle
	return float64(totalDelta-idleDelta) / float64(totalDelta) * 100
}

// cpuHwmonNames — известные chip-имена CPU-температурных драйверов; GPU-
// драйверы (radeon/amdgpu/nouveau/nvidia) сознательно не сюда — см. находку
// в gmp-agent/internal/collector (hwmon0=GPU против hwmon1=CPU).
var cpuHwmonNames = map[string]bool{
	"k10temp": true, "zenpower": true, "coretemp": true, "cpu_thermal": true,
}

func cpuTempC() float64 {
	if t, ok := readHwmonTemp(); ok {
		return t
	}
	if t, ok := readThermalZoneTemp(); ok {
		return t
	}
	return 0
}

func readHwmonTemp() (float64, bool) {
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return 0, false
	}
	var fallback float64
	haveFallback := false
	for _, e := range entries {
		dir := filepath.Join("/sys/class/hwmon", e.Name())
		name, _ := os.ReadFile(filepath.Join(dir, "name"))
		v, ok := readMilliDegreeFile(filepath.Join(dir, "temp1_input"))
		if !ok {
			continue
		}
		if cpuHwmonNames[strings.TrimSpace(string(name))] {
			return v, true
		}
		if !haveFallback {
			fallback, haveFallback = v, true
		}
	}
	return fallback, haveFallback
}

func readThermalZoneTemp() (float64, bool) {
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "thermal_zone") {
			continue
		}
		path := filepath.Join("/sys/class/thermal", e.Name(), "temp")
		if v, ok := readMilliDegreeFile(path); ok {
			return v, true
		}
	}
	return 0, false
}

func readMilliDegreeFile(path string) (float64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, false
	}
	return v / 1000, true
}

func uptimeSeconds() int64 {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

func memoryUsedPct() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	pct, err := parseMeminfoPct(f)
	if err != nil {
		return 0
	}
	return pct
}

func parseMeminfoPct(r io.Reader) (float64, error) {
	scanner := bufio.NewScanner(r)
	var total, available float64
	found := 0
	for scanner.Scan() {
		key, value, ok := parseMeminfoLine(scanner.Text())
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			total = value
			found++
		case "MemAvailable":
			available = value
			found++
		}
		if found == 2 {
			break
		}
	}
	if total == 0 {
		return 0, strconv.ErrSyntax
	}
	return (total - available) / total * 100, nil
}

func parseMeminfoLine(line string) (key string, value float64, ok bool) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSuffix(parts[0], ":"), v, true
}

func memTotalMB() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseMeminfoLine(scanner.Text())
		if ok && key == "MemTotal" {
			return value / 1024 // kB -> MB
		}
	}
	return 0
}

func swapUsedPct() (pct, totalMB float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var total, free float64
	scanner := bufio.NewScanner(f)
	found := 0
	for scanner.Scan() && found < 2 {
		key, value, ok := parseMeminfoLine(scanner.Text())
		if !ok {
			continue
		}
		switch key {
		case "SwapTotal":
			total = value
			found++
		case "SwapFree":
			free = value
			found++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return (total - free) / total * 100, total / 1024
}

func loadAvg() (load1, load5, load15 float64) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	load1, _ = strconv.ParseFloat(fields[0], 64)
	load5, _ = strconv.ParseFloat(fields[1], 64)
	load15, _ = strconv.ParseFloat(fields[2], 64)
	return load1, load5, load15
}

func cpuInfo() (cores int, mhz float64) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "processor") {
			cores++
		}
		if mhz == 0 && strings.HasPrefix(line, "cpu MHz") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				mhz, _ = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			}
		}
	}
	return cores, mhz
}

func diskUsedPct(path string) float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	free := float64(stat.Bfree) * float64(stat.Bsize)
	if total == 0 {
		return 0
	}
	return (total - free) / total * 100
}

func diskTotalGB(path string) float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	const bytesPerGB = 1024 * 1024 * 1024
	return float64(stat.Blocks) * float64(stat.Bsize) / bytesPerGB
}
