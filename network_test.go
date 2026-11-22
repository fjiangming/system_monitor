package main

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNetworkCountersAndDefaultRoutes(t *testing.T) {
	data := "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n  eth0: 1000 10 2 3 0 0 0 0 2000 20 4 5 0 0 0 0\n lo: 10 1 0 0 0 0 0 0 10 1 0 0 0 0 0 0\n"
	got, e := parseNetworkCounters(data)
	if e != nil || got["eth0"].values != [8]uint64{1000, 10, 2, 3, 2000, 20, 4, 5} {
		t.Fatalf("invalid counters: %+v %v", got, e)
	}
	for _, bad := range []string{"", "eth0: 1 2", strings.Replace(data, "1000", "oops", 1)} {
		if _, e := parseNetworkCounters(bad); e == nil {
			t.Fatal("accepted incomplete counters")
		}
	}
	ipv4 := "Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 00000000 01010101 0003 0 0 100 00000000\neth1 00000000 01010101 0000 0 0 100 00000000\ndocker0 000011AC 00000000 0001 0 0 0 0000FFFF\nlo 00000000 00000000 0201 0 0 0 00000000"
	ipv6 := "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000000 00000000 00000003 eth0\n00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000000 00000000 00000003 ens3\n00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000001 00200200 lo"
	if got := defaultNetworkInterfaces(ipv4, ipv6); !reflect.DeepEqual(got, []string{"ens3", "eth0"}) {
		t.Fatalf("wrong route selection: %v", got)
	}
	if got := defaultNetworkInterfaces("", ipv6); !reflect.DeepEqual(got, []string{"ens3", "eth0"}) {
		t.Fatalf("IPv6-only selection: %v", got)
	}
}

func TestNetworkRatesHandleResetsAndMissingInterfaces(t *testing.T) {
	at := time.Now()
	var sampler networkSampler
	first := map[string]networkCounters{"eth0": {index: 2, values: [8]uint64{1000, 10, 2, 3, 2000, 20, 4, 5}}}
	if got := sampler.sample(at, first, time.Minute); got[0].Ready {
		t.Fatal("first counters presented as a rate")
	}
	next := map[string]networkCounters{"eth0": {index: 2, values: [8]uint64{4000, 70, 5, 9, 8000, 50, 7, 11}}}
	got := sampler.sample(at.Add(3*time.Second), next, time.Minute)[0]
	if !got.Ready || got.ReceiveBytesPerSecond != 1000 || got.TransmitBytesPerSecond != 2000 || got.ReceivePacketsPerSecond != 20 || got.TransmitPacketsPerSecond != 10 || got.ReceiveDroppedPerSecond != 2 || got.TransmitErrorsPerSecond != 1 {
		t.Fatalf("wrong rates: %+v", got)
	}
	if got := sampler.sample(at.Add(4*time.Second), first, time.Minute)[0]; got.Ready || got.ReceiveBytesPerSecond != 0 {
		t.Fatal("counter reset produced a rate")
	}
	replaced := map[string]networkCounters{"eth0": {index: 3, values: next["eth0"].values}}
	if got := sampler.sample(at.Add(5*time.Second), replaced, time.Minute)[0]; got.Ready {
		t.Fatal("interface replacement reused old counters")
	}
	if got := sampler.sample(at.Add(2*time.Minute), replaced, time.Minute)[0]; got.Ready {
		t.Fatal("sampling gap reused old counters")
	}
	sampler.sample(at.Add(121*time.Second), map[string]networkCounters{}, time.Minute)
	if got := sampler.sample(at.Add(122*time.Second), replaced, time.Minute)[0]; got.Ready {
		t.Fatal("disappeared interface retained its baseline")
	}
	if _, ready := networkTotals([]NetworkInterface{{Name: "eth0", Ready: true, ReceiveBytesPerSecond: 10}, {Name: "eth1"}}); ready {
		t.Fatal("partial rates reported a complete total")
	}
}

func TestNetworkConfigurationAndLegacyServers(t *testing.T) {
	c := defaultServer()
	c.Name = "network-host"
	for _, interfaces := range []string{"eth0\nens3", ""} {
		c.Network.Interfaces = interfaces
		if e := c.Validate(); e != nil {
			t.Fatal(e)
		}
	}
	for _, interfaces := range []string{"eth0\neth0", "../eth0", "eth0; id", strings.Repeat("x", 16), strings.Repeat("eth0\n", 17)} {
		c.Network.Interfaces = interfaces
		if c.Validate() == nil {
			t.Fatalf("accepted interfaces %q", interfaces)
		}
	}
	c.Network.Interfaces = ""
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	legacy := map[string]any{}
	_ = json.Unmarshal([]byte(encode(v.Config)), &legacy)
	delete(legacy, "network")
	if _, e := s.DB.Exec(`UPDATE servers SET config=? WHERE id=?`, encode(legacy), id); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Server(id)
	if !v.Config.Network.Automatic {
		t.Fatalf("legacy server defaults: %+v", v.Config.Network)
	}
}

func TestNetworkHistoryUpgradeGapsAndRetention(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	if rows := s.History(id); rows[0]["receiveBytesPerSecond"] != nil {
		t.Fatal("legacy sample fabricated zero traffic")
	}
	// Reopening a database from the previous version adds only the new table.
	if _, e := s.DB.Exec(`DROP TABLE network_history`); e != nil {
		t.Fatal(e)
	}
	root := s.Root
	s.Close()
	var e error
	s, e = openStore(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	snap := Snapshot{Network: []NetworkInterface{{Name: "eth0", Ready: true, ReceiveBytesPerSecond: 1250000, TransmitBytesPerSecond: 250000, ReceivePacketsPerSecond: 4000}, {Name: "ens3", Ready: true, ReceiveBytesPerSecond: 125000, TransmitBytesPerSecond: 125000, ReceivePacketsPerSecond: 500}}}
	if e := s.SaveSample(id, &snap); e != nil {
		t.Fatal(e)
	}
	rows := s.History(id)
	if rows[0]["receiveBytesPerSecond"] != 1375000.0 || rows[0]["transmitBytesPerSecond"] != 375000.0 || rows[0]["receivePacketsPerSecond"] != 4500.0 {
		t.Fatalf("network history: %+v", rows[0])
	}
	snap.Network[0].Ready = false
	if e := s.SaveSample(id, &snap); e != nil {
		t.Fatal(e)
	}
	if s.History(id)[0]["receiveBytesPerSecond"] != nil {
		t.Fatal("unknown rate fabricated zero traffic")
	}
	if _, e := s.DB.Exec(`UPDATE history SET at=1 WHERE server_id=?`, id); e != nil {
		t.Fatal(e)
	}
	s.Prune()
	var count int
	if e := s.DB.QueryRow(`SELECT count(*) FROM network_history`).Scan(&count); e != nil || count != 0 {
		t.Fatal("network history escaped retention", count, e)
	}
	for _, bad := range []NetworkInterface{{Name: "../eth0"}, {Name: "eth0", ReceiveBytesPerSecond: -1}, {Name: "eth0", ReceiveBytesPerSecond: math.NaN()}, {Name: "eth0", ReceiveBytesPerSecond: math.Inf(1)}} {
		snap.Network = []NetworkInterface{bad}
		if e := s.SaveSample(id, &snap); e == nil {
			t.Fatalf("accepted invalid sample %+v", bad)
		}
	}
}

func TestNetworkAlertsRequireNewSustainedSamples(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	c, _ := s.Config()
	c.Alerts.Failures = 2
	c.Notifications.Notices = true
	c.Notifications.GroupWindow = 0
	v.Config.Network = NetworkConfig{Automatic: true}
	v.Snapshot.Network = []NetworkInterface{{Name: "eth0", Ready: true, ReceiveBytesPerSecond: 1250000, TransmitBytesPerSecond: 250000, ReceivePacketsPerSecond: 4000}}
	v.SampleAt = unixNow() - 70
	seedNetworkReference(t, s, v.ID, "eth0", v.SampleAt, NetworkRates{Receive: 125000, Transmit: 25000, Packets: 100})
	s.inspectServer(context.Background(), v, c)
	for i := 0; i < 3; i++ {
		s.inspectServer(context.Background(), v, c)
	}
	if len(s.Alerts(id)) != 0 {
		t.Fatal("old sample accelerated threshold")
	}
	v.SampleAt += 30
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 0 {
		t.Fatal("alert ignored sustained duration")
	}
	v.SampleAt += 30
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 3 {
		t.Fatalf("expected bandwidth and packet alerts: %+v", s.Alerts(id))
	}
	var notices int
	_ = s.DB.QueryRow(`SELECT count(*) FROM outbox`).Scan(&notices)
	if notices != 3 {
		t.Fatal("network alerts did not queue notifications", notices)
	}
	v.SampleAt++
	v.Snapshot.Network = nil
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 3 {
		t.Fatal("missing network data falsely recovered active alerts")
	}
	v.SampleAt++
	s.inspectServer(context.Background(), v, c)
	v.SampleAt++
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 4 {
		t.Fatal("old agent or missing sampling was not reported")
	}
	v.SampleAt++
	v.Snapshot.Network = []NetworkInterface{{Name: "eth0", Ready: true}}
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 0 {
		t.Fatal("valid low traffic failed to recover")
	}
}

func TestNetworkUnknownAndSelectionChangeInterruptPendingAlerts(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	c, _ := s.Config()
	c.Alerts.Failures = 2
	v.Config.Network = NetworkConfig{Interfaces: "eth0", Automatic: true}
	v.SampleAt = unixNow() - 70
	seedNetworkReference(t, s, v.ID, "eth0", v.SampleAt, NetworkRates{Receive: 125000, Transmit: 25000, Packets: 100})
	v.Snapshot.Network = []NetworkInterface{{Name: "eth0", Ready: true, ReceiveBytesPerSecond: 1250000}}
	s.inspectServer(context.Background(), v, c)
	v.SampleAt += 30
	v.Config.Network.Interfaces = "eth1"
	s.inspectServer(context.Background(), v, c)
	v.SampleAt += 30
	v.Config.Network.Interfaces = "eth0"
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 0 {
		t.Fatal("unknown sampling counted toward sustained traffic")
	}
}
