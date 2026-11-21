//go:build !linux && !windows

package main

import (
	"context"
	"errors"
)

type collector struct{}

func (p *collector) collect(ctx context.Context, c ServerConfig) Snapshot {
	return Snapshot{AgentVersion: version, Errors: []string{"探针仅支持 Linux"}}
}
func diskFree(path string) (uint64, error) {
	return 0, errors.New("disk capacity probe requires Linux")
}
