package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type networkCounters struct {
	index  int
	values [8]uint64
}

type networkSampler struct {
	at       time.Time
	previous map[string]networkCounters
}

func parseNetworkCounters(data string) (map[string]networkCounters, error) {
	out := map[string]networkCounters{}
	for _, line := range strings.Split(data, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fields := strings.Fields(rest)
		if len(fields) != 16 || name == "" {
			return nil, fmt.Errorf("invalid network counters")
		}
		var c networkCounters
		for i, field := range []int{0, 1, 2, 3, 8, 9, 10, 11} {
			n, e := strconv.ParseUint(fields[field], 10, 64)
			if e != nil {
				return nil, e
			}
			c.values[i] = n
		}
		out[name] = c
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no network counters")
	}
	return out, nil
}

func defaultNetworkInterfaces(ipv4, ipv6 string) []string {
	names := map[string]bool{}
	for _, line := range strings.Split(ipv4, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, e := strconv.ParseUint(f[3], 16, 32)
		if e == nil && flags&1 != 0 && flags&0x200 == 0 && f[0] != "lo" {
			names[f[0]] = true
		}
	}
	for _, line := range strings.Split(ipv6, "\n") {
		f := strings.Fields(line)
		if len(f) != 10 || f[0] != strings.Repeat("0", 32) || f[1] != "00" {
			continue
		}
		flags, e := strconv.ParseUint(f[8], 16, 32)
		if e == nil && flags&1 != 0 && flags&0x200 == 0 && f[9] != "lo" {
			names[f[9]] = true
		}
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (p *networkSampler) sample(now time.Time, current map[string]networkCounters, maxGap time.Duration) []NetworkInterface {
	out := make([]NetworkInterface, 0, len(current))
	elapsed := now.Sub(p.at)
	for name, c := range current {
		v := NetworkInterface{Name: name}
		before, ok := p.previous[name]
		v.Ready = ok && c.index == before.index && elapsed > 0 && elapsed <= maxGap
		for i, value := range c.values {
			if value < before.values[i] {
				v.Ready = false
			}
		}
		if v.Ready {
			rates := []*float64{&v.ReceiveBytesPerSecond, &v.ReceivePacketsPerSecond, &v.ReceiveErrorsPerSecond, &v.ReceiveDroppedPerSecond, &v.TransmitBytesPerSecond, &v.TransmitPacketsPerSecond, &v.TransmitErrorsPerSecond, &v.TransmitDroppedPerSecond}
			for i, rate := range rates {
				*rate = float64(c.values[i]-before.values[i]) / elapsed.Seconds()
			}
		}
		out = append(out, v)
	}
	p.at, p.previous = now, current
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func networkTotals(interfaces []NetworkInterface) (NetworkInterface, bool) {
	var total NetworkInterface
	if len(interfaces) == 0 {
		return total, false
	}
	for _, v := range interfaces {
		if !v.Ready {
			return NetworkInterface{}, false
		}
		total.ReceiveBytesPerSecond += v.ReceiveBytesPerSecond
		total.TransmitBytesPerSecond += v.TransmitBytesPerSecond
		total.ReceivePacketsPerSecond += v.ReceivePacketsPerSecond
		total.TransmitPacketsPerSecond += v.TransmitPacketsPerSecond
	}
	return total, true
}

func validNetworkSample(interfaces []NetworkInterface) bool {
	names := map[string]bool{}
	if len(interfaces) > 16 {
		return false
	}
	for _, v := range interfaces {
		if !interfaceNamePattern.MatchString(v.Name) || names[v.Name] {
			return false
		}
		names[v.Name] = true
		for _, rate := range []float64{v.ReceiveBytesPerSecond, v.TransmitBytesPerSecond, v.ReceivePacketsPerSecond, v.TransmitPacketsPerSecond, v.ReceiveDroppedPerSecond, v.TransmitDroppedPerSecond, v.ReceiveErrorsPerSecond, v.TransmitErrorsPerSecond} {
			if rate < 0 || rate > 1e15 || math.IsNaN(rate) || math.IsInf(rate, 0) {
				return false
			}
		}
	}
	return true
}
