package main

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	stripLabels  = []string{"host", "strip", "model"}
	outletLabels = []string{"host", "strip", "model", "outlet", "outlet_index"}

	stripUpDesc = prometheus.NewDesc("tapo_strip_up",
		"Whether the last poll of the strip succeeded and is recent (1) or not (0).", stripLabels, nil)
	stripInfoDesc = prometheus.NewDesc("tapo_strip_info",
		"Static strip details; always 1.", append(stripLabels, "device_id", "mac", "hardware_version", "software_version"), nil)
	stripRSSIDesc = prometheus.NewDesc("tapo_strip_rssi_dbm",
		"Wi-Fi signal strength reported by the strip.", stripLabels, nil)
	stripLastSuccessDesc = prometheus.NewDesc("tapo_strip_last_success_timestamp_seconds",
		"Unix time of the last successful poll.", stripLabels, nil)
	stripPollDurationDesc = prometheus.NewDesc("tapo_strip_poll_duration_seconds",
		"Duration of the most recent poll.", stripLabels, nil)

	outletOnDesc = prometheus.NewDesc("tapo_outlet_on",
		"Whether the outlet relay is on (1) or off (0).", outletLabels, nil)
	outletOnTimeDesc = prometheus.NewDesc("tapo_outlet_on_time_seconds",
		"How long the outlet has been on.", outletLabels, nil)
	outletPowerDesc = prometheus.NewDesc("tapo_outlet_power_watts",
		"Real-time power draw.", outletLabels, nil)
	outletVoltageDesc = prometheus.NewDesc("tapo_outlet_voltage_volts",
		"Real-time voltage.", outletLabels, nil)
	outletCurrentDesc = prometheus.NewDesc("tapo_outlet_current_amps",
		"Real-time current.", outletLabels, nil)
	outletEnergyDesc = prometheus.NewDesc("tapo_outlet_energy_kwh_total",
		"Energy meter total reported by the device; may reset, so use increase().", outletLabels, nil)
)

// collector reads the manager's cached snapshots; scrapes never touch devices.
type collector struct{ manager *manager }

func (c collector) Describe(ch chan<- *prometheus.Desc) { prometheus.DescribeByCollect(c, ch) }

func (c collector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	for _, status := range c.manager.statuses() {
		strip, model := status.Config.Alias, status.Config.Model
		if status.Snapshot != nil {
			strip, model = status.Snapshot.Device.Alias, status.Snapshot.Device.Model
		}
		labels := []string{status.Host, strip, model}

		fresh := c.manager.fresh(status, now)
		ch <- prometheus.MustNewConstMetric(stripUpDesc, prometheus.GaugeValue, boolValue(fresh), labels...)
		if !status.LastSuccess.IsZero() {
			ch <- prometheus.MustNewConstMetric(stripLastSuccessDesc, prometheus.GaugeValue,
				float64(status.LastSuccess.UnixNano())/1e9, labels...)
		}
		if status.Duration > 0 {
			ch <- prometheus.MustNewConstMetric(stripPollDurationDesc, prometheus.GaugeValue, status.Duration.Seconds(), labels...)
		}
		if !fresh {
			continue
		}

		device := status.Snapshot.Device
		ch <- prometheus.MustNewConstMetric(stripInfoDesc, prometheus.GaugeValue, 1,
			append(labels, device.DeviceID, device.MAC, device.HardwareVersion, device.SoftwareVersion)...)
		ch <- prometheus.MustNewConstMetric(stripRSSIDesc, prometheus.GaugeValue, float64(device.RSSI), labels...)

		for _, outlet := range status.Snapshot.Outlets {
			outletValues := append(append([]string(nil), labels...), outlet.Alias, strconv.Itoa(outlet.Index+1))
			ch <- prometheus.MustNewConstMetric(outletOnDesc, prometheus.GaugeValue, boolValue(outlet.On), outletValues...)
			ch <- prometheus.MustNewConstMetric(outletOnTimeDesc, prometheus.GaugeValue, outlet.OnTime.Seconds(), outletValues...)
			if outlet.Energy == nil {
				continue
			}
			ch <- prometheus.MustNewConstMetric(outletPowerDesc, prometheus.GaugeValue, outlet.Energy.Power, outletValues...)
			ch <- prometheus.MustNewConstMetric(outletVoltageDesc, prometheus.GaugeValue, outlet.Energy.Voltage, outletValues...)
			ch <- prometheus.MustNewConstMetric(outletCurrentDesc, prometheus.GaugeValue, outlet.Energy.Current, outletValues...)
			ch <- prometheus.MustNewConstMetric(outletEnergyDesc, prometheus.CounterValue, outlet.Energy.Total, outletValues...)
		}
	}
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
