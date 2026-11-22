package main

import (
	"context"
	"reflect"
	"testing"
)

func insertNetworkObservation(t *testing.T, s *Store, id, signature string, at int64, rates NetworkRates, accepted bool) {
	t.Helper()
	tx, e := s.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	row, e := tx.Exec(`INSERT INTO history(server_id,at,cpu,memory,restarts) VALUES(?,?,0,0,'{}')`, id, at)
	if e != nil {
		t.Fatal(e)
	}
	sampleID, e := row.LastInsertId()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`INSERT INTO network_history(sample_id,receive,transmit,packets) VALUES(?,?,?,?)`, sampleID, rates.Receive, rates.Transmit, rates.Packets); e != nil {
		t.Fatal(e)
	}
	if accepted {
		if _, e = tx.Exec(`INSERT INTO network_learning(sample_id,interfaces) VALUES(?,?)`, sampleID, signature); e != nil {
			t.Fatal(e)
		}
	}
	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}
}

func seedNetworkReference(t *testing.T, s *Store, id, signature string, before int64, rates NetworkRates) {
	t.Helper()
	for i := 0; i <= 60; i++ {
		insertNetworkObservation(t, s, id, signature, before-int64(61-i)*30, rates, true)
	}
}

func TestAutomaticNetworkBaselineWarmupNoiseAndSpikes(t *testing.T) {
	samples := []networkReference{}
	for i := 0; i < 62; i++ {
		rates := NetworkRates{Receive: 125000, Transmit: 25000, Packets: 100}
		if i == 10 {
			rates = NetworkRates{Receive: 12500000, Transmit: 2500000, Packets: 10000}
		}
		samples = append(samples, networkReference{at: 10000 + int64(i)*30, NetworkRates: rates})
	}
	if status := automaticNetworkBaseline(samples[:30]); status.State != "learning" || status.Progress >= 100 {
		t.Fatalf("short history treated as a learned baseline: %+v", status)
	}
	if status := automaticNetworkBaseline([]networkReference{samples[0], samples[61]}); status.State != "learning" {
		t.Fatal("two distant samples bypassed the sample requirement")
	}
	status := automaticNetworkBaseline(samples)
	if status.State != "normal" || status.Progress != 100 || status.Baseline.Receive != 125000 || status.Limits.Receive != 500000 || status.Limits.Packets != 1000 {
		t.Fatalf("isolated spike contaminated the robust range: %+v", status)
	}
	for i := range samples {
		samples[i].NetworkRates = NetworkRates{}
	}
	status = automaticNetworkBaseline(samples)
	if status.Limits.Receive != 125000 || status.Limits.Transmit != 125000 || status.Limits.Packets != 1000 {
		t.Fatal("idle-host baseline has no noise floor", status)
	}
}

func TestAutomaticNetworkHysteresisAndBackup(t *testing.T) {
	status := NetworkStatus{State: "normal", Interfaces: "eth0", Progress: 100, Limits: NetworkRates{Receive: 500000, Transmit: 125000, Packets: 1000}}
	high := assessNetwork(status, NetworkStatus{}, NetworkInterface{ReceiveBytesPerSecond: 1000000}, false)
	if !high.Frozen || !reflect.DeepEqual(high.Elevated, []string{"receive"}) {
		t.Fatal("large increase did not freeze the baseline", high)
	}
	near := assessNetwork(high, high, NetworkInterface{ReceiveBytesPerSecond: 450000}, false)
	if !near.Frozen {
		t.Fatal("a small fluctuation caused recovery")
	}
	low := assessNetwork(near, near, NetworkInterface{ReceiveBytesPerSecond: 250000}, false)
	if low.Frozen || low.State != "normal" {
		t.Fatal("clear recovery did not release the baseline")
	}
	backup := assessNetwork(status, NetworkStatus{}, NetworkInterface{TransmitBytesPerSecond: 1000000}, true)
	if backup.Frozen {
		t.Fatal("new outbound backup activity triggered a traffic anomaly")
	}
	prior := assessNetwork(status, NetworkStatus{}, NetworkInterface{TransmitBytesPerSecond: 1000000}, false)
	if next := assessNetwork(prior, prior, NetworkInterface{TransmitBytesPerSecond: 1000000}, true); !next.Frozen {
		t.Fatal("starting a backup hid an existing traffic anomaly")
	}
	if next := assessNetwork(status, NetworkStatus{}, NetworkInterface{ReceiveBytesPerSecond: 1000000}, true); !next.Frozen {
		t.Fatal("a backup suppressed inbound anomaly detection")
	}
}

func TestAutomaticNetworkLearnsNormalSamplesAndFreezesDuringIncident(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	c, _ := s.Config()
	c.Notifications.Notices = true
	baseTime := unixNow() - 2200
	normal := NetworkRates{Receive: 125000, Transmit: 25000, Packets: 100}
	feed := func(at int64, rates NetworkRates) {
		t.Helper()
		v.SampleAt = at
		v.Snapshot.Network = []NetworkInterface{{Name: "eth0", Ready: true, ReceiveBytesPerSecond: rates.Receive, TransmitBytesPerSecond: rates.Transmit, ReceivePacketsPerSecond: rates.Packets}}
		insertNetworkObservation(t, s, id, "eth0", at, rates, false)
		s.inspectNetwork(v, c, map[string]bool{})
	}
	for i := 0; i < 62; i++ {
		feed(baseTime+int64(i)*30, normal)
	}
	status := s.previousNetworkStatus(id)
	if status.State != "normal" || status.Progress != 100 {
		t.Fatal("normal history did not complete automatic learning", status)
	}
	for i := 0; i < 5; i++ {
		s.inspectNetwork(v, c, map[string]bool{})
	}
	var trained int
	_ = s.DB.QueryRow(`SELECT count(*) FROM network_learning`).Scan(&trained)
	if trained != 62 {
		t.Fatal("repeated processing trained duplicate observations", trained)
	}
	high := NetworkRates{Receive: 1250000, Transmit: normal.Transmit, Packets: normal.Packets}
	feed(baseTime+1860, high)
	for i := 0; i < 5; i++ {
		s.inspectNetwork(v, c, map[string]bool{})
	}
	if len(s.Alerts(id)) != 0 {
		t.Fatal("one sample or repeated inspection triggered an alert")
	}
	feed(baseTime+1890, high)
	if len(s.Alerts(id)) != 0 {
		t.Fatal("two samples bypassed the sustained check")
	}
	feed(baseTime+1920, high)
	if len(s.Alerts(id)) != 1 {
		t.Fatal("automatic anomaly did not alert", s.Alerts(id))
	}
	_ = s.DB.QueryRow(`SELECT count(*) FROM network_learning`).Scan(&trained)
	if trained != 62 {
		t.Fatal("anomalous samples entered the baseline", trained)
	}
	frozen := s.previousNetworkStatus(id)
	// Retention and a restart must not turn a long incident into normal traffic.
	if _, e := s.DB.Exec(`DELETE FROM history WHERE server_id=?`, id); e != nil {
		t.Fatal(e)
	}
	_ = s.DB.QueryRow(`SELECT count(*) FROM network_learning`).Scan(&trained)
	if trained != 0 {
		t.Fatal("learning records escaped cascading history retention", trained)
	}
	root := s.Root
	s.Close()
	var e error
	s, e = openStore(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	feed(baseTime+networkReferenceSeconds+3000, high)
	after := s.previousNetworkStatus(id)
	if !after.Frozen || after.Limits != frozen.Limits || after.State != "elevated" {
		t.Fatal("long incident was learned as normal", after)
	}
	feed(v.SampleAt+30, normal)
	if len(s.Alerts(id)) != 0 || s.previousNetworkStatus(id).Frozen {
		t.Fatal("healthy traffic did not recover")
	}
	var notices int
	_ = s.DB.QueryRow(`SELECT count(*) FROM outbox`).Scan(&notices)
	if notices != 2 {
		t.Fatal("expected one automatic alert and its recovery", notices)
	}
}

func TestAutomaticNetworkUnknownMaintenanceAndInterfaceChange(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	c, _ := s.Config()
	v.SampleAt = unixNow()
	v.MaintenanceUntil = unixNow() + 600
	rates := NetworkRates{Receive: 125000, Transmit: 25000, Packets: 100}
	v.Snapshot.Network = []NetworkInterface{{Name: "eth0", Ready: true, ReceiveBytesPerSecond: rates.Receive, TransmitBytesPerSecond: rates.Transmit, ReceivePacketsPerSecond: rates.Packets}}
	insertNetworkObservation(t, s, id, "eth0", v.SampleAt, rates, false)
	s.inspectNetwork(v, c, map[string]bool{})
	var trained int
	_ = s.DB.QueryRow(`SELECT count(*) FROM network_learning`).Scan(&trained)
	if trained != 0 {
		t.Fatal("maintenance activity trained the normal baseline")
	}
	v.MaintenanceUntil = 0
	v.Snapshot.Network[0].Ready = false
	s.inspectNetwork(v, c, map[string]bool{})
	if status := s.previousNetworkStatus(id); status.State != "unknown" {
		t.Fatal("unknown rates reported healthy", status)
	}
	seedNetworkReference(t, s, id, "eth0", v.SampleAt, rates)
	v.Snapshot.Network[0].Ready = true
	v.Snapshot.Network[0].Name = "ens3"
	s.inspectNetwork(v, c, map[string]bool{})
	if status := s.previousNetworkStatus(id); status.State != "learning" || status.Progress != 0 || status.Interfaces != "ens3" {
		t.Fatal("a different interface reused the previous baseline", status)
	}
	v.Config.Network.Automatic = false
	s.inspectServer(context.Background(), v, c)
	if len(s.Alerts(id)) != 0 {
		t.Fatal("disabled automatic monitoring retained alerts")
	}
}
