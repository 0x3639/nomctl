package metrics

import (
	"testing"
	"time"
)

const root = "testdata/proc"

func TestProcReaders(t *testing.T) {
	st, err := ReadProcStatus(root, 1234)
	if err != nil || st.Name != "znnd" || st.Threads != 38 || st.VmRSS != 1900000*1024 || st.VmPeak != 3200000*1024 {
		t.Errorf("status = %+v, %v", st, err)
	}
	s, err := ReadProcStat(root, 1234)
	if err != nil || s.UTime != 1000 || s.STime != 500 {
		t.Errorf("stat = %+v, %v", s, err)
	}
	pio, err := ReadProcIO(root, 1234)
	if err != nil || pio.ReadBytes != 1048576 || pio.WriteBytes != 2097152 {
		t.Errorf("io = %+v, %v", pio, err)
	}
	lim, err := ReadFDLimit(root, 1234)
	if err != nil || lim != 32768 {
		t.Errorf("fd limit = %d, %v", lim, err)
	}
	n, err := CountFDs(root, 1234)
	if err != nil || n != 3 {
		t.Errorf("fds = %d, %v", n, err)
	}
	if _, err := ReadProcStatus(root, 1); err == nil {
		t.Error("missing pid must error")
	}
}

func TestHostReaders(t *testing.T) {
	l1, l5, l15, err := ReadLoadAvg(root)
	if err != nil || l1 != 1.2 || l5 != 0.9 || l15 != 0.8 {
		t.Errorf("loadavg = %v %v %v %v", l1, l5, l15, err)
	}
	m, err := ReadMemInfo(root)
	if err != nil || m.Total != 8000000*1024 || m.Available != 3100000*1024 {
		t.Errorf("meminfo = %+v, %v", m, err)
	}
	p := ReadPressure(root)
	if p.CPU != 2.1 || p.IO != 15.4 || p.Memory != 0 {
		t.Errorf("pressure = %+v", p)
	}
	if p := ReadPressure("testdata/nowhere"); p != (Pressure{}) {
		t.Errorf("missing pressure files must be zero, got %+v", p)
	}
}

func TestCgroup(t *testing.T) {
	c := ReadCgroup("testdata/cgroup", "/system.slice/go-zenon.service")
	if !c.Present || c.MemoryCurrent != 1990000000 || c.MemoryPeak != 2100000000 || c.MemoryMax != 0 || c.PidsCurrent != 38 {
		t.Errorf("cgroup = %+v", c)
	}
	if ReadCgroup("testdata/cgroup", "").Present {
		t.Error("empty control group must not be present")
	}
}

func TestParseServiceProps(t *testing.T) {
	p := ParseServiceProps("ActiveState=active\nSubState=running\nMainPID=1234\nNRestarts=2\nResult=success\nControlGroup=/system.slice/go-zenon.service\nExecMainStartTimestamp=Sat 2026-09-06 10:00:00 UTC\n")
	if p.ActiveState != "active" || p.MainPID != 1234 || p.NRestarts != 2 || p.ControlGroup != "/system.slice/go-zenon.service" {
		t.Errorf("props = %+v", p)
	}
	want := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	if !p.ExecMainStart.Equal(want) {
		t.Errorf("start = %v, want %v", p.ExecMainStart, want)
	}
	if !ParseServiceProps("ExecMainStartTimestamp=\n").ExecMainStart.IsZero() {
		t.Error("empty timestamp must be zero")
	}
}
