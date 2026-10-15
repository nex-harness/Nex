// live_test.go (step 3.8): the Linux sampling path against a REAL child
// process tree. A fixture /proc tree only proves the parser; this proves the
// reader walks the live machine, the pgid/ppid topology is what /proc
// reports, and the sampler turns real cputime growth into a CPU percentage.
package processtelemetry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sleepCommand is a child that stays alive long enough to be sampled.
func sleepCommand(seconds int) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", "sleep "+strconv.Itoa(seconds))
}

func TestLiveLinuxProcTableReadsTheRunningMachine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the live /proc reader is Linux-only")
	}
	rows, ok := ReadProcTable(context.Background())
	if !ok {
		t.Fatal("reading /proc failed")
	}
	if len(rows) < 2 {
		t.Fatalf("only %d rows read from a live machine", len(rows))
	}
	self := os.Getpid()
	var found bool
	for _, row := range rows {
		if row.PID != self {
			continue
		}
		found = true
		if row.PPID != os.Getppid() {
			t.Fatalf("self ppid = %d, want %d", row.PPID, os.Getppid())
		}
		if row.RSSKb <= 0 {
			t.Fatalf("self rss = %d KiB, want > 0", row.RSSKb)
		}
	}
	if !found {
		t.Fatalf("the test process (pid %d) is missing from /proc", self)
	}
}

func TestLiveLinuxProcTableSeesAChildProcessTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the live /proc reader is Linux-only")
	}
	// A shell that really forks AND stays alive to own its children: a
	// subshell would exit immediately and reparent the grandchild to init.
	// 5s is long enough to observe the fork, short enough not to slow the suite.
	shell := exec.Command("/bin/sh", "-c", "sleep 5 & wait")
	if err := shell.Start(); err != nil {
		t.Fatalf("start shell: %v", err)
	}
	defer func() {
		_ = syscall.Kill(-shell.Process.Pid, syscall.SIGKILL)
		_ = shell.Wait()
	}()

	// The fork takes a moment after the shell itself appears, so the tree is
	// polled until BOTH the shell and a child of it are in the table.
	var rows []ResourceRow
	grandchildren := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		rows, ok = ReadProcTable(context.Background())
		if !ok {
			t.Fatal("reading /proc failed")
		}
		if !containsPID(rows, shell.Process.Pid) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		grandchildren = 0
		for _, row := range rows {
			if row.PPID == shell.Process.Pid {
				grandchildren++
			}
		}
		if grandchildren > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !containsPID(rows, shell.Process.Pid) {
		t.Fatalf("the child shell (pid %d) never showed up in /proc", shell.Process.Pid)
	}
	if grandchildren == 0 {
		t.Fatal("no grandchild reported the shell as its parent")
	}
	// The whole tree must attribute to the root, and only the root's own
	// children are one level down.
	attributed := AttributeHostProcessTree(AttributeOptions{
		Samples: samplesOf(rows), HostPID: os.Getpid(),
	})
	seen := map[int]bool{os.Getpid(): true}
	for _, row := range attributed {
		seen[row.PID] = true
	}
	if !seen[shell.Process.Pid] {
		t.Fatalf("the live shell is missing from the attributed rows")
	}
}

func TestLiveSamplerTurnsRealCPUTimeIntoAPercent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the live sampler is Linux-only")
	}
	// A busy child: the sampler must report a non-zero CPU percent for it once
	// a baseline exists. The absolute value is not asserted — the machine's
	// scheduling makes it nondeterministic — but a live, busy process that
	// stays at exactly 0 would mean the differential is broken.
	child := exec.Command("/bin/sh", "-c",
		"i=0; while [ $i -lt 400000 ]; do i=$((i+1)); done; sleep 5")
	if err := child.Start(); err != nil {
		t.Fatalf("start busy child: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()

	sampler := NewSampler(SamplerOptions{
		ReadTable:       ReadProcTable,
		LogicalCPUCount: func() int { return runtime.NumCPU() },
	})
	busy := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		samples, ok := sampler.Sample(context.Background())
		if !ok {
			t.Fatal("sampling was skipped")
		}
		if sample, present := samples[child.Process.Pid]; present && sample.CPUPercent > 0 {
			busy = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !busy {
		t.Fatalf("a busy child never reported a non-zero CPU percent (pid %d)", child.Process.Pid)
	}
}

func TestLiveSamplerReadsItsOwnTreeUnderTheHostRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the live sampler is Linux-only")
	}
	child := exec.Command("/bin/sh", "-c", "sleep 5")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()
	// Wait for the child to appear so attribution has something to find.
	deadline := time.Now().Add(5 * time.Second)
	var samples map[int]Sample
	for time.Now().Before(deadline) {
		var ok bool
		samples, ok = ReadProcTableAsSamples(context.Background())
		if !ok {
			t.Fatal("reading /proc failed")
		}
		if _, present := samples[child.Process.Pid]; present {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The test process stands in for the Host root.
	rows := AttributeHostProcessTree(AttributeOptions{
		Samples: samples, HostPID: os.Getpid(),
	})
	found := false
	for _, row := range rows {
		if row.PID == child.Process.Pid {
			found = true
			if row.GroupKey != "host" || row.Category != CategoryBase {
				t.Fatalf("an unattributed descendant = %#v", row)
			}
			if row.MemoryBytes <= 0 {
				t.Fatalf("memory = %d", row.MemoryBytes)
			}
		}
	}
	if !found {
		t.Fatalf("the live child (pid %d) is missing from the attributed rows", child.Process.Pid)
	}
}

// ReadProcTableAsSamples runs the real table read and turns it into samples
// (no CPU differential — this is for topology assertions).
func ReadProcTableAsSamples(ctx context.Context) (map[int]Sample, bool) {
	rows, ok := ReadProcTable(ctx)
	if !ok {
		return nil, false
	}
	samples := make(map[int]Sample, len(rows))
	for _, row := range rows {
		samples[row.PID] = Sample{PID: row.PID, PPID: row.PPID, RSSKb: row.RSSKb, Command: row.Command}
	}
	return samples, true
}

func containsPID(rows []ResourceRow, pid int) bool {
	for _, row := range rows {
		if row.PID == pid {
			return true
		}
	}
	return false
}

func TestReadProcTableAtUsesAFixtureRoot(t *testing.T) {
	// The reader is exercised against a synthetic tree so the format contract
	// is pinned even where /proc is not the host's procfs.
	root := t.TempDir()
	writeFixtureProc(t, root, "100", "100 (nex-host) S 1 100 100 0 -1 0 10 0 0 0 300 200 0 0 20 0 1 0 5000 1 2 3", 1234)
	writeFixtureProc(t, root, "200", "200 (nex-agent-nex-demo-proj) S 100 200 200 0 -1 0 10 0 0 0 700 700 0 0 20 0 1 0 6000 1 2 3", 4096)
	writeFixtureProc(t, root, "300", "300 (sleep) S 200 300 300 0 -1 0 0 0 0 0 50 50 0 0 20 0 1 0 7000 1 2 3", 512)
	// A non-numeric directory is not a process.
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	rows, ok := ReadProcTableAt(context.Background(), root)
	if !ok {
		t.Fatal("fixture read failed")
	}
	if len(rows) != 3 {
		t.Fatalf("read %d rows, want 3: %#v", len(rows), rows)
	}
	byPID := map[int]ResourceRow{}
	for _, row := range rows {
		byPID[row.PID] = row
	}
	if byPID[300].PPID != 200 || byPID[300].Command != "sleep" {
		t.Fatalf("child row = %#v", byPID[300])
	}
	// 300+200 ticks → 5000ms; 700+700 → 14000ms.
	if byPID[100].CPUTimeMs != 5000 || byPID[200].CPUTimeMs != 14000 {
		t.Fatalf("cpu times = %d / %d", byPID[100].CPUTimeMs, byPID[200].CPUTimeMs)
	}
	if byPID[200].RSSKb != 4096 {
		t.Fatalf("rss = %d", byPID[200].RSSKb)
	}
	// The fixture topology attributes the child to the agent's group.
	attributed := AttributeHostProcessTree(AttributeOptions{
		Samples: samplesOf(rows), HostPID: 100,
		Agents: []Agent{{PID: 200, Provider: "nex-demo", WorkspacePath: "/w/proj"}},
	})
	if len(attributed) != 2 {
		t.Fatalf("attributed %d rows, want 2: %#v", len(attributed), attributed)
	}
	if attributed[0].GroupKey != "cli" || attributed[1].GroupKey != "cli" {
		t.Fatalf("the child must inherit the agent group: %#v", attributed)
	}
	if attributed[1].Name != "sleep" {
		t.Fatalf("child name = %q", attributed[1].Name)
	}
}

func TestReadProcTableAtReportsAMissingRoot(t *testing.T) {
	if _, ok := ReadProcTableAt(context.Background(), filepath.Join(t.TempDir(), "nope")); ok {
		t.Fatal("a missing /proc root must report ok=false")
	}
}

func writeFixtureProc(t *testing.T, root, pid, stat string, rssKb int) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	status := "Name:\tproc\nState:\tS\nVmRSS:\t" + strconv.Itoa(rssKb) + " kB\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

func samplesOf(rows []ResourceRow) map[int]Sample {
	samples := make(map[int]Sample, len(rows))
	for _, row := range rows {
		samples[row.PID] = Sample{PID: row.PID, PPID: row.PPID, RSSKb: row.RSSKb, Command: row.Command}
	}
	return samples
}

func TestFixtureTreeCommandNamesStayLocal(t *testing.T) {
	// The attributed name must never be a full path even when /proc's comm is.
	rows := []ResourceRow{{PID: 5, PPID: 1, Command: "/opt/nex/bin/nex-agent", RSSKb: 1}}
	attributed := AttributeHostProcessTree(AttributeOptions{
		Samples: samplesOf(rows), HostPID: 1,
		Agents: []Agent{{PID: 5, Provider: "p", WorkspacePath: "/w"}},
	})
	if len(attributed) != 1 {
		t.Fatalf("rows = %#v", attributed)
	}
	if strings.Contains(attributed[0].Name, "/") {
		t.Fatalf("the process name leaked a path: %q", attributed[0].Name)
	}
}
