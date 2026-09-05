package tapo

import (
	"encoding/json"
	"fmt"
	"time"
)

// DeviceInfo describes the power strip itself.
type DeviceInfo struct {
	Alias           string `json:"alias"`
	Model           string `json:"model"`
	DeviceID        string `json:"device_id"`
	MAC             string `json:"mac"`
	HardwareVersion string `json:"hardware_version"`
	SoftwareVersion string `json:"software_version"`
	RSSI            int    `json:"rssi"`
}

// Outlet describes one controllable socket on a strip. Energy is nil when
// meter data was not requested or is unavailable.
type Outlet struct {
	Index  int           `json:"index"`
	ID     string        `json:"id"`
	Alias  string        `json:"alias"`
	On     bool          `json:"on"`
	OnTime time.Duration `json:"on_time"`
	Energy *Energy       `json:"energy,omitempty"`
}

// Energy is a normalized real-time meter reading. Devices may report base or
// milli-units on the wire; these fields are always SI units (and kWh total).
type Energy struct {
	Voltage float64 `json:"voltage_v"`
	Current float64 `json:"current_a"`
	Power   float64 `json:"power_w"`
	Total   float64 `json:"total_kwh"`
}

// Snapshot is a point-in-time view of a strip and all of its outlets.
type Snapshot struct {
	Device    DeviceInfo `json:"device"`
	Outlets   []Outlet   `json:"outlets"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// DailyUsage is one daily energy counter returned by the device.
type DailyUsage struct {
	Day    int     `json:"day"`
	Energy float64 `json:"energy_kwh"`
}

// MonthlyUsage is one monthly energy counter returned by the device.
type MonthlyUsage struct {
	Month  time.Month `json:"month"`
	Energy float64    `json:"energy_kwh"`
}

// DeviceError is an error response returned by the device.
type DeviceError struct {
	Module  string
	Command string
	Code    int
	Message string
}

func (e *DeviceError) Error() string {
	where := e.Module + "." + e.Command
	if e.Message != "" {
		return fmt.Sprintf("tapo: %s failed (%d): %s", where, e.Code, e.Message)
	}
	return fmt.Sprintf("tapo: %s failed with device error %d", where, e.Code)
}

type errorFields struct {
	ErrorCode int    `json:"err_code"`
	ErrorMsg  string `json:"err_msg"`
}

func checkDeviceError(module, command string, e errorFields) error {
	if e.ErrorCode == 0 {
		return nil
	}
	return &DeviceError{Module: module, Command: command, Code: e.ErrorCode, Message: e.ErrorMsg}
}

type wireSystemInfo struct {
	errorFields
	Alias    string `json:"alias"`
	Model    string `json:"model"`
	DeviceID string `json:"deviceId"`
	MAC      string `json:"mac"`
	HWVer    string `json:"hw_ver"`
	SWVer    string `json:"sw_ver"`
	RSSI     int    `json:"rssi"`
	Children []struct {
		ID     string `json:"id"`
		Alias  string `json:"alias"`
		State  int    `json:"state"`
		OnTime int64  `json:"on_time"`
	} `json:"children"`
}

func (w wireSystemInfo) public() (DeviceInfo, []Outlet) {
	info := DeviceInfo{
		Alias: w.Alias, Model: w.Model, DeviceID: w.DeviceID, MAC: w.MAC,
		HardwareVersion: w.HWVer, SoftwareVersion: w.SWVer, RSSI: w.RSSI,
	}
	outlets := make([]Outlet, len(w.Children))
	for i, child := range w.Children {
		outlets[i] = Outlet{
			Index: i, ID: child.ID, Alias: child.Alias, On: child.State != 0,
			OnTime: time.Duration(child.OnTime) * time.Second,
		}
	}
	return info, outlets
}

type wireEnergy struct {
	errorFields
	Voltage   *float64 `json:"voltage"`
	VoltageMV *float64 `json:"voltage_mv"`
	Current   *float64 `json:"current"`
	CurrentMA *float64 `json:"current_ma"`
	Power     *float64 `json:"power"`
	PowerMW   *float64 `json:"power_mw"`
	Total     *float64 `json:"total"`
	TotalWH   *float64 `json:"total_wh"`
}

func (w wireEnergy) public() Energy {
	return Energy{
		Voltage: normalized(w.Voltage, w.VoltageMV, 1000),
		Current: normalized(w.Current, w.CurrentMA, 1000),
		Power:   normalized(w.Power, w.PowerMW, 1000),
		Total:   normalized(w.Total, w.TotalWH, 1000),
	}
}

func normalized(base, milli *float64, divisor float64) float64 {
	if base != nil {
		return *base
	}
	if milli != nil {
		return *milli / divisor
	}
	return 0
}

type wireUsage struct {
	Day      int      `json:"day"`
	Month    int      `json:"month"`
	Energy   *float64 `json:"energy"`
	EnergyWH *float64 `json:"energy_wh"`
}

func (w wireUsage) kWh() float64 { return normalized(w.Energy, w.EnergyWH, 1000) }

// RawResponse is useful for diagnostic commands not covered by the high-level
// API. Callers should normally use Snapshot, Energy, DailyEnergy, and
// MonthlyEnergy instead.
type RawResponse map[string]json.RawMessage
