package main

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
)

const (
	networkLearningSamples  = 30
	networkLearningSeconds  = 30 * 60
	networkReferenceSeconds = 24 * 60 * 60
	networkAutoFailures     = 3
	networkAutoSustained    = 60
)

type NetworkRates struct {
	Receive  float64 `json:"receive"`
	Transmit float64 `json:"transmit"`
	Packets  float64 `json:"packets"`
}

type NetworkStatus struct {
	State      string       `json:"state"`
	At         int64        `json:"at"`
	Interfaces string       `json:"interfaces"`
	Samples    int          `json:"samples"`
	Progress   int          `json:"progress"`
	Baseline   NetworkRates `json:"baseline"`
	Limits     NetworkRates `json:"limits"`
	Elevated   []string     `json:"elevated"`
	Frozen     bool         `json:"frozen"`
}

type networkReference struct {
	at int64
	NetworkRates
}

func networkSignature(interfaces []NetworkInterface) string {
	names := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		names = append(names, iface.Name)
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}

func quantile(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	position := fraction * float64(len(sorted)-1)
	lo, hi := int(math.Floor(position)), int(math.Ceil(position))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(position-float64(lo))
}

func automaticNetworkRange(values []float64, floor float64) (float64, float64) {
	sort.Float64s(values)
	median := quantile(values, 0.5)
	deviations := make([]float64, len(values))
	for i, value := range values {
		deviations[i] = math.Abs(value - median)
	}
	sort.Float64s(deviations)
	// Robust spread and the observed upper range tolerate ordinary busy periods.
	limit := max(floor, median*4, quantile(values, 0.95)*1.5, median+6*1.4826*quantile(deviations, 0.5))
	return median, limit
}

func automaticNetworkBaseline(samples []networkReference) NetworkStatus {
	status := NetworkStatus{State: "learning", Samples: len(samples)}
	if len(samples) == 0 {
		return status
	}
	receive, transmit, packets := []float64{}, []float64{}, []float64{}
	first, last := samples[0].at, samples[0].at
	for _, sample := range samples {
		first, last = min(first, sample.at), max(last, sample.at)
		receive = append(receive, sample.Receive)
		transmit = append(transmit, sample.Transmit)
		packets = append(packets, sample.Packets)
	}
	status.Progress = min(100, len(samples)*100/networkLearningSamples, int((last-first)*100/networkLearningSeconds))
	status.Baseline.Receive, status.Limits.Receive = automaticNetworkRange(receive, 125000)
	status.Baseline.Transmit, status.Limits.Transmit = automaticNetworkRange(transmit, 125000)
	status.Baseline.Packets, status.Limits.Packets = automaticNetworkRange(packets, 1000)
	if status.Progress == 100 {
		status.State = "normal"
	}
	return status
}

func (s *Store) networkReference(serverID, signature string, at int64) ([]networkReference, error) {
	// Only inspected, accepted observations train the model; the current point is excluded.
	rows, e := s.DB.Query(`SELECT h.at,n.receive,n.transmit,n.packets FROM history h JOIN network_history n ON n.sample_id=h.id JOIN network_learning l ON l.sample_id=n.sample_id WHERE h.server_id=? AND l.interfaces=? AND h.at>=? AND h.at<? ORDER BY h.at DESC,h.id DESC LIMIT 2880`, serverID, signature, at-networkReferenceSeconds, at)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	samples := []networkReference{}
	var previous int64 = -1
	for rows.Next() {
		var sample networkReference
		if e := rows.Scan(&sample.at, &sample.Receive, &sample.Transmit, &sample.Packets); e != nil {
			return nil, e
		}
		if sample.at != previous {
			samples = append(samples, sample)
			previous = sample.at
		}
	}
	return samples, rows.Err()
}

func (s *Store) previousNetworkStatus(id string) NetworkStatus {
	var raw string
	_ = s.DB.QueryRow(`SELECT status FROM network_status WHERE server_id=?`, id).Scan(&raw)
	var status NetworkStatus
	_ = json.Unmarshal([]byte(raw), &status)
	return status
}

func (s *Store) saveNetworkStatus(v Server, status NetworkStatus, accept bool, total NetworkInterface) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e := tx.Exec(`INSERT INTO network_status(server_id,status) VALUES(?,?) ON CONFLICT(server_id) DO UPDATE SET status=excluded.status`, v.ID, encode(status)); e != nil {
		return e
	}
	if accept && v.MaintenanceUntil < unixNow() {
		_, e = tx.Exec(`INSERT OR IGNORE INTO network_learning(sample_id,interfaces) SELECT n.sample_id,? FROM network_history n JOIN history h ON h.id=n.sample_id WHERE h.server_id=? AND h.at=? AND n.receive=? AND n.transmit=? AND n.packets=? ORDER BY h.id DESC LIMIT 1`, status.Interfaces, v.ID, v.SampleAt, total.ReceiveBytesPerSecond, total.TransmitBytesPerSecond, total.ReceivePacketsPerSecond)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}

func networkWasElevated(status NetworkStatus, key string) bool {
	for _, elevated := range status.Elevated {
		if key == elevated {
			return true
		}
	}
	return false
}

func assessNetwork(status, previous NetworkStatus, total NetworkInterface, backup bool) NetworkStatus {
	status.Elevated = nil
	for _, metric := range []struct {
		key          string
		value, limit float64
	}{
		{"receive", total.ReceiveBytesPerSecond, status.Limits.Receive},
		{"transmit", total.TransmitBytesPerSecond, status.Limits.Transmit},
		{"packets", total.ReceivePacketsPerSecond, status.Limits.Packets},
	} {
		wasHigh := previous.Frozen && previous.Interfaces == status.Interfaces && networkWasElevated(previous, metric.key)
		if backup && metric.key == "transmit" && !wasHigh {
			continue
		}
		limit := metric.limit
		if wasHigh {
			limit *= 0.75
		}
		if metric.value >= limit {
			status.Elevated = append(status.Elevated, metric.key)
		}
	}
	status.Frozen = len(status.Elevated) > 0
	status.State = "normal"
	if status.Frozen {
		status.State = "elevated"
	}
	return status
}
