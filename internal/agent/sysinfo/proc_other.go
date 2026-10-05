//go:build !linux

package sysinfo

// The agent only runs on Linux; these stubs keep the module building elsewhere.

func osName() string                        { return "unsupported" }
func kernelRelease() string                 { return "" }
func cpuTimes() (idle, total uint64)        { return 0, 0 }
func memInfo() mem                          { return mem{} }
func diskUsage(string) (total, used uint64) { return 0, 0 }
func loadAvg() (l1, l5, l15 float64)        { return }
func netBytes() (rx, tx uint64)             { return }
func uptime() int64                         { return 0 }

type mem struct{ total, available uint64 }
