package codex

import (
	"os"
	"strings"
	"testing"
)

func TestParseProcStatHandlesSpacesInProcessName(t *testing.T) {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[1] = "123"
	fields[19] = "98765"
	ppid, start, err := parseProcStat("42 (codex worker) " + strings.Join(fields, " "))
	if err != nil || ppid != 123 || start != "98765" {
		t.Fatalf("ppid=%d start=%q err=%v", ppid, start, err)
	}
}

func TestOwnerUsesProcessStartTime(t *testing.T) {
	_, start, err := procInfo(os.Getpid())
	if err != nil {
		t.Skipf("/proc unavailable: %v", err)
	}
	if !ownerAlive(os.Getpid(), start) {
		t.Fatal("live owner not recognized")
	}
	if ownerAlive(os.Getpid(), "wrong-start") {
		t.Fatal("reused PID not rejected")
	}
}
