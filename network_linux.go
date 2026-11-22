//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func (p *networkSampler) collect(c ServerConfig) ([]NetworkInterface, error) {
	data, e := os.ReadFile("/proc/net/dev")
	if e != nil {
		p.previous = nil
		return nil, fmt.Errorf("网络计数器不可读取")
	}
	all, e := parseNetworkCounters(string(data))
	if e != nil {
		p.previous = nil
		return nil, fmt.Errorf("网络计数器无效")
	}
	names := lines(c.Network.Interfaces)
	if len(names) == 0 {
		ipv4, _ := os.ReadFile("/proc/net/route")
		ipv6, _ := os.ReadFile("/proc/net/ipv6_route")
		names = defaultNetworkInterfaces(string(ipv4), string(ipv6))
	}
	if len(names) == 0 || len(names) > 16 {
		p.previous = nil
		return nil, fmt.Errorf("未找到默认路由网卡，请在流量监控中选择网卡")
	}
	current := map[string]networkCounters{}
	missing := []string{}
	for _, name := range names {
		counter, ok := all[name]
		iface, err := net.InterfaceByName(name)
		if !ok || err != nil {
			missing = append(missing, name)
			continue
		}
		counter.index = iface.Index
		current[name] = counter
	}
	out := p.sample(time.Now(), current, time.Duration(c.HeartbeatTimeout)*time.Second)
	if len(missing) > 0 {
		for _, name := range missing {
			out = append(out, NetworkInterface{Name: name})
		}
		return out, fmt.Errorf("网卡不可读取：%s", strings.Join(missing, "、"))
	}
	return out, nil
}
