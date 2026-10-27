package tapo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// modernRequest is the RPC envelope used by current Tapo devices such as the
// P316M. The KLAP transport encrypts this JSON but does not otherwise change it.
type modernRequest struct {
	Method string `json:"method"`
	Params any    `json:"params"`
}

type modernResponse[T any] struct {
	ErrorCode int    `json:"error_code"`
	ErrorMsg  string `json:"msg,omitempty"`
	Result    *T     `json:"result,omitempty"`
}

func (s *Strip) ensureAPIMode(ctx context.Context) (uint32, error) {
	if mode := s.apiMode.Load(); mode != deviceAPIUnknown {
		return mode, nil
	}
	if _, _, err := s.systemInfo(ctx); err != nil {
		return deviceAPIUnknown, err
	}
	return s.apiMode.Load(), nil
}

func modernCall[T any](ctx context.Context, s *Strip, request modernRequest) (T, error) {
	var response modernResponse[T]
	if err := s.query(ctx, request, &response); err != nil {
		var zero T
		return zero, err
	}
	if err := checkModernDeviceError(request.Method, response.ErrorCode, response.ErrorMsg); err != nil {
		var zero T
		return zero, err
	}
	if response.Result == nil {
		var zero T
		return zero, fmt.Errorf("tapo: response is missing %s result", request.Method)
	}
	return *response.Result, nil
}

func (s *Strip) modernCommand(ctx context.Context, request modernRequest) error {
	var response modernResponse[json.RawMessage]
	if err := s.query(ctx, request, &response); err != nil {
		return err
	}
	return checkModernDeviceError(request.Method, response.ErrorCode, response.ErrorMsg)
}

func checkModernDeviceError(method string, code int, message string) error {
	if code == 0 {
		return nil
	}
	return &DeviceError{Command: method, Code: code, Message: message}
}

type modernDeviceInfo struct {
	Nickname string `json:"nickname"`
	Model    string `json:"model"`
	DeviceID string `json:"device_id"`
	MAC      string `json:"mac"`
	HWVer    string `json:"hw_ver"`
	FWVer    string `json:"fw_ver"`
	RSSI     int    `json:"rssi"`
	DeviceOn bool   `json:"device_on"`
	OnTime   int64  `json:"on_time"`
}

type modernChild struct {
	Nickname string `json:"nickname"`
	DeviceID string `json:"device_id"`
	DeviceOn bool   `json:"device_on"`
	OnTime   int64  `json:"on_time"`
	Position int    `json:"position"`
}

type modernChildList struct {
	Children []modernChild `json:"child_device_list"`
}

func (s *Strip) modernSystemInfo(ctx context.Context) (DeviceInfo, []Outlet, error) {
	infoWire, err := modernCall[modernDeviceInfo](ctx, s, modernRequest{
		Method: "get_device_info", Params: nil,
	})
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	alias := decodeModernString(infoWire.Nickname)
	if alias == "" {
		alias = infoWire.Model
	}
	info := DeviceInfo{
		Alias: alias, Model: infoWire.Model, DeviceID: infoWire.DeviceID, MAC: infoWire.MAC,
		HardwareVersion: infoWire.HWVer, SoftwareVersion: infoWire.FWVer, RSSI: infoWire.RSSI,
	}

	mode, err := s.modernDeviceMode(ctx)
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	if mode == deviceAPIModernPlug {
		if infoWire.DeviceID == "" {
			return DeviceInfo{}, nil, errors.New("tapo: get_device_info response is missing device_id")
		}
		// Publish the ID before the mode so that any caller observing plug
		// mode can also route by ID.
		s.plugID.Store(&infoWire.DeviceID)
		s.apiMode.CompareAndSwap(deviceAPIUnknown, deviceAPIModernPlug)
		outlets := []Outlet{{
			Index: 0, ID: infoWire.DeviceID, Alias: alias, On: infoWire.DeviceOn,
			OnTime: time.Duration(infoWire.OnTime) * time.Second,
		}}
		return info, outlets, nil
	}

	childrenWire, err := modernCall[modernChildList](ctx, s, modernRequest{
		Method: "get_child_device_list", Params: map[string]int{"start_index": 0},
	})
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	s.apiMode.CompareAndSwap(deviceAPIUnknown, deviceAPIModern)
	sort.SliceStable(childrenWire.Children, func(i, j int) bool {
		return childrenWire.Children[i].Position < childrenWire.Children[j].Position
	})
	outlets := make([]Outlet, len(childrenWire.Children))
	for i, child := range childrenWire.Children {
		childAlias := decodeModernString(child.Nickname)
		if childAlias == "" {
			childAlias = fmt.Sprintf("Outlet %d", i+1)
		}
		outlets[i] = Outlet{
			Index: i, ID: child.DeviceID, Alias: childAlias, On: child.DeviceOn,
			OnTime: time.Duration(child.OnTime) * time.Second,
		}
	}
	return info, outlets, nil
}

type modernComponents struct {
	Components []struct {
		ID      string `json:"id"`
		VerCode int    `json:"ver_code"`
	} `json:"component_list"`
}

// modernDeviceMode reports whether a modern device is a multi-outlet strip or
// a single plug. Strips advertise the control_child component; a device that
// does not, such as the P110M, is a plug. Devices that reject component
// negotiation are assumed to be strips, which was the only modern device type
// supported before plugs.
func (s *Strip) modernDeviceMode(ctx context.Context) (uint32, error) {
	if mode := s.apiMode.Load(); mode == deviceAPIModern || mode == deviceAPIModernPlug {
		return mode, nil
	}
	result, err := modernCall[modernComponents](ctx, s, modernRequest{Method: "component_nego", Params: nil})
	if err != nil {
		var deviceErr *DeviceError
		if errors.As(err, &deviceErr) {
			return deviceAPIModern, nil
		}
		return deviceAPIUnknown, err
	}
	for _, component := range result.Components {
		if component.ID == "control_child" {
			return deviceAPIModern, nil
		}
	}
	return deviceAPIModernPlug, nil
}

func decodeModernString(value string) string {
	if value == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return value
	}
	return string(decoded)
}

type modernMultipleResult[T any] struct {
	Responses []modernResponse[T] `json:"responses"`
}

type modernResponseData[T any] struct {
	Result modernMultipleResult[T] `json:"result"`
}

type modernControlResult[T any] struct {
	ResponseData modernResponseData[T] `json:"responseData"`
}

// modernControlChild sends child to one outlet. On a strip it is wrapped in
// control_child; on a plug the only outlet is the device itself, so child is
// sent directly.
func modernControlChild[T any](ctx context.Context, s *Strip, outletID string, child modernRequest) (*T, error) {
	if s.apiMode.Load() == deviceAPIModernPlug {
		if id := s.plugID.Load(); id == nil || *id != outletID {
			return nil, fmt.Errorf("tapo: unknown outlet ID %q", outletID)
		}
		// Setters reply with only an error code, so a missing result is not
		// an error here; callers that need one check for nil.
		var response modernResponse[T]
		if err := s.query(ctx, child, &response); err != nil {
			return nil, err
		}
		if err := checkModernDeviceError(child.Method, response.ErrorCode, response.ErrorMsg); err != nil {
			return nil, err
		}
		return response.Result, nil
	}
	requestData := modernRequest{
		Method: "multipleRequest",
		Params: map[string]any{"requests": []modernRequest{child}},
	}
	result, err := modernCall[modernControlResult[T]](ctx, s, modernRequest{
		Method: "control_child",
		Params: map[string]any{"device_id": outletID, "requestData": requestData},
	})
	if err != nil {
		return nil, err
	}
	responses := result.ResponseData.Result.Responses
	if len(responses) == 0 {
		return nil, errors.New("tapo: control_child response contains no child responses")
	}
	response := responses[0]
	if err := checkModernDeviceError(child.Method, response.ErrorCode, response.ErrorMsg); err != nil {
		return nil, err
	}
	return response.Result, nil
}

type modernEnergy struct {
	VoltageMV *float64 `json:"voltage_mv"`
	CurrentMA *float64 `json:"current_ma"`
	PowerMW   *float64 `json:"power_mw"`
	EnergyWH  *float64 `json:"energy_wh"`
}

func (s *Strip) modernEnergy(ctx context.Context, outletID string) (Energy, error) {
	if s.plugNoEmeter.Load() {
		return s.plugEnergyUsage(ctx, outletID)
	}
	result, err := modernControlChild[modernEnergy](ctx, s, outletID, modernRequest{
		Method: "get_emeter_data", Params: nil,
	})
	var deviceErr *DeviceError
	if err != nil && errors.As(err, &deviceErr) && s.apiMode.Load() == deviceAPIModernPlug {
		// Plugs with version 1 of the energy_monitoring component only
		// report power; remember that so later reads skip the failing call.
		energy, usageErr := s.plugEnergyUsage(ctx, outletID)
		if usageErr != nil {
			return Energy{}, errors.Join(err, usageErr)
		}
		s.plugNoEmeter.Store(true)
		return energy, nil
	}
	if err != nil {
		return Energy{}, err
	}
	if result == nil {
		return Energy{}, errors.New("tapo: response is missing get_emeter_data result")
	}
	return Energy{
		Voltage: valueOrZero(result.VoltageMV) / 1000,
		Current: valueOrZero(result.CurrentMA) / 1000,
		Power:   valueOrZero(result.PowerMW) / 1000,
		Total:   valueOrZero(result.EnergyWH) / 1000,
	}, nil
}

type modernEnergyUsage struct {
	CurrentPowerMW *float64 `json:"current_power"`
}

// plugEnergyUsage reads power from get_energy_usage, which every metering plug
// supports. It has no voltage, current, or lifetime total, so those are zero.
func (s *Strip) plugEnergyUsage(ctx context.Context, outletID string) (Energy, error) {
	result, err := modernControlChild[modernEnergyUsage](ctx, s, outletID, modernRequest{
		Method: "get_energy_usage", Params: nil,
	})
	if err != nil {
		return Energy{}, err
	}
	if result == nil || result.CurrentPowerMW == nil {
		return Energy{}, errors.New("tapo: response is missing get_energy_usage current_power")
	}
	return Energy{Power: *result.CurrentPowerMW / 1000}, nil
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Strip) modernSetOutlets(ctx context.Context, outletIDs []string, on bool) error {
	var errs []error
	for _, outletID := range outletIDs {
		_, err := modernControlChild[json.RawMessage](ctx, s, outletID, modernRequest{
			Method: "set_device_info", Params: map[string]bool{"device_on": on},
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("outlet %s: %w", outletID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Strip) modernRenameOutlet(ctx context.Context, outletID, alias string) error {
	_, err := modernControlChild[json.RawMessage](ctx, s, outletID, modernRequest{
		Method: "set_device_info",
		Params: map[string]string{"nickname": base64.StdEncoding.EncodeToString([]byte(alias))},
	})
	return err
}

func (s *Strip) modernSetLED(ctx context.Context, enabled bool) error {
	settings, err := modernCall[map[string]any](ctx, s, modernRequest{
		Method: "get_led_info", Params: nil,
	})
	if err != nil {
		return err
	}
	if enabled {
		settings["led_rule"] = "always"
	} else {
		settings["led_rule"] = "never"
	}
	return s.modernCommand(ctx, modernRequest{Method: "set_led_info", Params: settings})
}

type modernEnergyData struct {
	Data           []float64 `json:"data"`
	StartTimestamp int64     `json:"start_timestamp"`
	Interval       int       `json:"interval"`
}

func (s *Strip) modernEnergyData(ctx context.Context, outletID string, start time.Time, interval int) (modernEnergyData, error) {
	result, err := modernControlChild[modernEnergyData](ctx, s, outletID, modernRequest{
		Method: "get_energy_data",
		Params: map[string]any{
			"start_timestamp": start.Unix(),
			"end_timestamp":   start.Unix(),
			"interval":        interval,
		},
	})
	if err != nil {
		return modernEnergyData{}, err
	}
	if result == nil {
		return modernEnergyData{}, errors.New("tapo: response is missing get_energy_data result")
	}
	return *result, nil
}

func (s *Strip) modernDailyEnergy(ctx context.Context, outletID string, date time.Time) ([]DailyUsage, error) {
	location := date.Location()
	quarterMonth := time.Month(((int(date.Month()) - 1) / 3 * 3) + 1)
	start := time.Date(date.Year(), quarterMonth, 1, 0, 0, 0, 0, location)
	data, err := s.modernEnergyData(ctx, outletID, start, 1440)
	if err != nil {
		return nil, err
	}
	cursor := start
	if data.StartTimestamp != 0 {
		cursor = time.Unix(data.StartTimestamp, 0).In(location)
	}
	days := make([]DailyUsage, 0, 31)
	for i, energyWH := range data.Data {
		day := cursor.AddDate(0, 0, i)
		if day.Year() == date.Year() && day.Month() == date.Month() {
			days = append(days, DailyUsage{Day: day.Day(), Energy: energyWH / 1000})
		}
	}
	return days, nil
}

func (s *Strip) modernMonthlyEnergy(ctx context.Context, outletID string, year int) ([]MonthlyUsage, error) {
	start := time.Date(year, time.January, 1, 0, 0, 0, 0, time.Local)
	data, err := s.modernEnergyData(ctx, outletID, start, 43200)
	if err != nil {
		return nil, err
	}
	cursor := start
	if data.StartTimestamp != 0 {
		cursor = time.Unix(data.StartTimestamp, 0).In(time.Local)
	}
	months := make([]MonthlyUsage, 0, 12)
	for i, energyWH := range data.Data {
		month := cursor.AddDate(0, i, 0)
		if month.Year() == year {
			months = append(months, MonthlyUsage{Month: month.Month(), Energy: energyWH / 1000})
		}
	}
	return months, nil
}
