package metrics

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// System is the server's load as Linux reports it; on other systems only the Go runtime part is filled.
type System struct {
	CPUPercent  float64 `json:"cpu_percent"` // all cores, since the previous reading (at least a second apart)
	Load1       float64 `json:"load1"`
	Cores       int     `json:"cores"`
	MemUsedMB   float64 `json:"mem_used_mb"`
	MemTotalMB  float64 `json:"mem_total_mb"`
	ProcessMB   float64 `json:"process_mb"` // memory the Go runtime holds from the OS
	Goroutines  int     `json:"goroutines"`
	LinuxFigure bool    `json:"linux"`
}

var cpu struct {
	sync.Mutex
	idle, total uint64
	at          time.Time
	last        float64
}

// ReadSystem reads /proc on Linux; everywhere else it reports the runtime figures only.
func ReadSystem() System {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := System{Cores: runtime.NumCPU(), Goroutines: runtime.NumGoroutine(), ProcessMB: float64(ms.Sys) / (1 << 20)}
	if runtime.GOOS != "linux" {
		return s
	}
	s.LinuxFigure = true
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			s.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		var total, avail float64
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			v, _ := strconv.ParseFloat(fields[1], 64)
			switch fields[0] {
			case "MemTotal:":
				total = v
			case "MemAvailable:":
				avail = v
			}
		}
		_ = f.Close()
		s.MemTotalMB, s.MemUsedMB = total/1024, (total-avail)/1024
	}
	s.CPUPercent = cpuPercent()
	return s
}

func cpuPercent() float64 {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 { // idle and iowait
			idle += n
		}
	}
	cpu.Lock()
	defer cpu.Unlock()
	if time.Since(cpu.at) < time.Second {
		return cpu.last
	}
	if cpu.total > 0 && total > cpu.total {
		dt, di := float64(total-cpu.total), float64(idle-cpu.idle)
		cpu.last = (1 - di/dt) * 100
	}
	cpu.idle, cpu.total, cpu.at = idle, total, time.Now()
	return cpu.last
}
