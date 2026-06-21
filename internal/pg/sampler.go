package pg

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/telemetry"
)

// sampleInterval is the short window over which rate metrics (CPU%, TPS) are
// measured: two reads bracket a brief sleep and the delta is scaled per second.
const sampleInterval = 150 * time.Millisecond

// Sampler produces a telemetry.Snapshot from host /proc counters and Postgres
// statistics read over the read-only pool. It implements telemetry.Sampler.
//
// Reads are best-effort: a metric that cannot be read (no /proc on the platform,
// Postgres unreachable) is left at its zero value rather than failing the whole
// snapshot, so the dashboard still renders what is available.
type Sampler struct {
	mgr *Manager
}

// NewSampler builds a Sampler over a Manager (for the Postgres reads).
func NewSampler(mgr *Manager) *Sampler {
	return &Sampler{mgr: mgr}
}

// pgCounters are the cumulative Postgres counters used to derive a TPS rate.
type pgCounters struct {
	xact float64 // xact_commit + xact_rollback across all databases
}

// Sample collects host and Postgres metrics into a Snapshot. It brackets a short
// sleep to compute CPU% and TPS as rates.
func (s *Sampler) Sample(ctx context.Context) (telemetry.Snapshot, error) {
	snap := telemetry.Snapshot{TakenAt: time.Now().UTC()}

	// --- host: instantaneous gauges ---
	if total, used, ok := readMemInfo(); ok {
		snap.MemTotalBytes = total
		snap.MemUsedBytes = used
	}
	if total, used, ok := readDiskUsage(s.dataDir()); ok {
		snap.DiskTotalBytes = total
		snap.DiskUsedBytes = used
	}
	if load1, ok := readLoad1(); ok {
		snap.Load1 = load1
	}

	// --- rate brackets: CPU and TPS ---
	cpuBusy0, cpuTotal0, cpuOK := readCPUTimes()
	pg0, pgOK := s.readPGCounters(ctx)

	// instantaneous Postgres gauges (connections, cache hit, deadlocks, lag)
	s.readPGGauges(ctx, &snap)

	select {
	case <-ctx.Done():
		return snap, nil
	case <-time.After(sampleInterval):
	}

	if cpuOK {
		if cpuBusy1, cpuTotal1, ok := readCPUTimes(); ok {
			if dt := cpuTotal1 - cpuTotal0; dt > 0 {
				snap.CPUPercent = float64(cpuBusy1-cpuBusy0) / float64(dt) * 100
			}
		}
	}
	if pgOK {
		if pg1, ok := s.readPGCounters(ctx); ok {
			secs := sampleInterval.Seconds()
			if secs > 0 {
				tps := (pg1.xact - pg0.xact) / secs
				if tps > 0 {
					snap.TPS = tps
				}
			}
		}
	}

	return snap, nil
}

// dataDir returns a filesystem path on the Postgres data volume for the disk
// usage read. The socket dir is on the same host; the conventional data dir is
// preferred and the socket dir's parent is the fallback.
func (s *Sampler) dataDir() string {
	for _, p := range []string{"/var/lib/postgresql", s.mgr.socketDir(), "/"} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/"
}

// readMemInfo reads total and used memory (bytes) from /proc/meminfo. Used is
// MemTotal - MemAvailable, the figure that reflects real pressure.
func readMemInfo() (total, used int64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	var memTotal, memAvail int64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotal = parseMeminfoKB(line)
			haveTotal = true
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvail = parseMeminfoKB(line)
			haveAvail = true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal || !haveAvail || memTotal <= 0 {
		// Without MemAvailable we cannot compute real usage; report unknown
		// rather than falsely showing 100% used (memAvail would be 0).
		return 0, 0, false
	}
	used = memTotal - memAvail
	if used < 0 {
		used = 0
	}
	return memTotal, used, true
}

// parseMeminfoKB parses the kB value from a /proc/meminfo line ("MemTotal: 12345
// kB") and returns it in bytes.
func parseMeminfoKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return kb * 1024
}

// readDiskUsage returns total and used bytes for the filesystem containing path.
func readDiskUsage(path string) (total, used int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bsize := int64(st.Bsize)
	total = int64(st.Blocks) * bsize
	if total <= 0 {
		return 0, 0, false
	}
	// Used excludes free blocks (including root-reserved ones), matching `df`'s
	// used figure; using Bavail here would count reserved blocks as used and
	// inflate the percentage.
	used = (int64(st.Blocks) - int64(st.Bfree)) * bsize
	if used < 0 {
		used = 0
	}
	return total, used, true
}

// readLoad1 reads the 1-minute load average from /proc/loadavg.
func readLoad1() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readCPUTimes returns cumulative busy and total CPU jiffies from the aggregate
// "cpu" line of /proc/stat. busy = total - (idle + iowait).
func readCPUTimes() (busy, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:] // drop "cpu"
		var idle uint64
		for i, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				continue
			}
			// Fields 8 (guest) and 9 (guest_nice) are already counted
			// inside user (field 0) and nice (field 1) respectively, so
			// skip them to avoid double-counting total.
			if i >= 8 {
				continue
			}
			total += v
			// idle is field 3 (idle) and 4 (iowait), 0-indexed.
			if i == 3 || i == 4 {
				idle += v
			}
		}
		if total == 0 {
			return 0, 0, false
		}
		return total - idle, total, true
	}
	return 0, 0, false
}

// readPGCounters reads cumulative transaction counters for the TPS rate. Returns
// ok=false when Postgres is unreachable.
func (s *Sampler) readPGCounters(ctx context.Context) (pgCounters, bool) {
	pool := s.mgr.ReadPool()
	if pool == nil {
		return pgCounters{}, false
	}
	var c pgCounters
	const q = `SELECT COALESCE(SUM(xact_commit + xact_rollback), 0)::float8 FROM pg_stat_database`
	if err := pool.QueryRow(ctx, q).Scan(&c.xact); err != nil {
		return pgCounters{}, false
	}
	return c, true
}

// readPGGauges fills the instantaneous Postgres metrics on snap: connections,
// max connections, cache hit ratio, deadlocks, and replication lag. Each read is
// independent and best-effort.
func (s *Sampler) readPGGauges(ctx context.Context, snap *telemetry.Snapshot) {
	pool := s.mgr.ReadPool()
	if pool == nil {
		return
	}

	var conns, maxConns int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM pg_stat_activity`).Scan(&conns); err == nil {
		snap.Connections = conns
	}
	if err := pool.QueryRow(ctx, `SELECT current_setting('max_connections')::int`).Scan(&maxConns); err == nil {
		snap.MaxConnections = maxConns
	}

	var hitRatio float64
	const cacheQ = `
SELECT COALESCE(SUM(blks_hit)::float8 / NULLIF(SUM(blks_hit + blks_read), 0), 0)
FROM pg_stat_database`
	if err := pool.QueryRow(ctx, cacheQ).Scan(&hitRatio); err == nil {
		snap.CacheHitRatio = hitRatio
	}

	var deadlocks int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(deadlocks), 0)::bigint FROM pg_stat_database`).Scan(&deadlocks); err == nil {
		snap.Deadlocks = deadlocks
	}

	var lag float64
	const lagQ = `SELECT COALESCE(EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp())), 0)::float8`
	if err := pool.QueryRow(ctx, lagQ).Scan(&lag); err == nil {
		snap.ReplicationLagSeconds = lag
	}
}
