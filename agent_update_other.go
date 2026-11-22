//go:build !linux

package main

import (
	"context"
	"errors"
)

func superviseAgent(context.Context, string) error {
	return errors.New("探针托管服务仅支持 Linux")
}
func reportAgentUpdateRecovery(context.Context, *Store, agentIdentity, ServerConfig) {}
func acknowledgeAgentUpdate(*Store, *Snapshot)                                       {}
func performAgentUpdate(context.Context, *Store, agentIdentity, ServerConfig, AgentUpdate) error {
	return errors.New("探针更新仅支持 Linux")
}
