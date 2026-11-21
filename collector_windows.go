package main

import (
	"context"
	"golang.org/x/sys/windows"
)

type collector struct{}

func (p *collector) collect(ctx context.Context, c ServerConfig) Snapshot {
	return Snapshot{AgentVersion: version, Errors: []string{"探针仅支持 Linux"}}
}
func diskFree(path string) (uint64, error) {
	p, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return 0, e
	}
	var free, total, available uint64
	e = windows.GetDiskFreeSpaceEx(p, &free, &total, &available)
	return free, e
}
