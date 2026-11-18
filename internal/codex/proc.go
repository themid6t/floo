package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procInfo reads Linux's stable process start tick and parent PID. On other
// systems it returns an error and the owner check is disabled.
func procInfo(pid int) (ppid int, start string, err error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	return parseProcStat(string(raw))
}

func parseProcStat(raw string) (ppid int, start string, err error) {
	end := strings.LastIndex(raw, ")")
	if end < 0 {
		return 0, "", fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) < 20 {
		return 0, "", fmt.Errorf("short proc stat")
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", err
	}
	return ppid, fields[19], nil
}

func findCodexOwner(pid int) (int, string) {
	for i := 0; i < 12 && pid > 1; i++ {
		ppid, start, err := procInfo(pid)
		if err != nil {
			return 0, ""
		}
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err == nil && filepath.Base(exe) == "codex" {
			return pid, start
		}
		pid = ppid
	}
	return 0, ""
}

func ownerAlive(pid int, start string) bool {
	if pid == 0 {
		return true
	} // no reliable owner found at hook time
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(raw)[end+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return false
	}
	return fields[19] == start
}
