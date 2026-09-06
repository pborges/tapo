package tapo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type systemInfoResponse struct {
	ErrorCode int `json:"error_code"`
	System    struct {
		GetSysInfo *wireSystemInfo `json:"get_sysinfo"`
	} `json:"system"`
}

var errLegacyAPIUnsupported = errors.New("tapo: legacy device API is unsupported")

const (
	deviceAPIUnknown uint32 = iota
	deviceAPILegacy
	deviceAPIModern
)

func (s *Strip) systemInfo(ctx context.Context) (DeviceInfo, []Outlet, error) {
	switch s.apiMode.Load() {
	case deviceAPILegacy:
		return s.legacySystemInfo(ctx)
	case deviceAPIModern:
		return s.modernSystemInfo(ctx)
	}

	info, outlets, err := s.legacySystemInfo(ctx)
	if err == nil {
		s.apiMode.CompareAndSwap(deviceAPIUnknown, deviceAPILegacy)
		return info, outlets, nil
	}
	if !errors.Is(err, errLegacyAPIUnsupported) {
		return DeviceInfo{}, nil, err
	}

	info, outlets, err = s.modernSystemInfo(ctx)
	if err != nil {
		return DeviceInfo{}, nil, fmt.Errorf("tapo: detect modern device API: %w", err)
	}
	s.apiMode.CompareAndSwap(deviceAPIUnknown, deviceAPIModern)
	return info, outlets, nil
}

func (s *Strip) legacySystemInfo(ctx context.Context) (DeviceInfo, []Outlet, error) {
	request := map[string]any{"system": map[string]any{"get_sysinfo": nil}}
	var response systemInfoResponse
	if err := s.query(ctx, request, &response); err != nil {
		return DeviceInfo{}, nil, err
	}
	if response.ErrorCode != 0 {
		return DeviceInfo{}, nil, errLegacyAPIUnsupported
	}
	if response.System.GetSysInfo == nil {
		return DeviceInfo{}, nil, errLegacyAPIUnsupported
	}
	wire := *response.System.GetSysInfo
	if err := checkDeviceError("system", "get_sysinfo", wire.errorFields); err != nil {
		return DeviceInfo{}, nil, err
	}
	info, outlets := wire.public()
	return info, outlets, nil
}

// Info returns current information about the strip.
func (s *Strip) Info(ctx context.Context) (DeviceInfo, error) {
	info, _, err := s.systemInfo(ctx)
	return info, err
}

// Outlets returns current outlet names, identifiers, states, and on-times. It
// does not query energy meters; use Snapshot when both are needed.
func (s *Strip) Outlets(ctx context.Context) ([]Outlet, error) {
	_, outlets, err := s.systemInfo(ctx)
	return outlets, err
}

// Snapshot returns current device, outlet, and per-outlet meter data. If one or
// more meter reads fail, it returns the usable partial snapshot together with a
// joined error; failed outlets have nil Energy fields.
func (s *Strip) Snapshot(ctx context.Context) (*Snapshot, error) {
	info, outlets, err := s.systemInfo(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Device: info, Outlets: outlets, UpdatedAt: time.Now()}
	meterCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, len(outlets))
	for i := range outlets {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			energy, err := s.Energy(meterCtx, outlets[i].ID)
			if err != nil {
				errs[i] = fmt.Errorf("outlet %d (%s): %w", i+1, outlets[i].Alias, err)
				return
			}
			snapshot.Outlets[i].Energy = &energy
		}()
	}
	wg.Wait()
	return snapshot, errors.Join(errs...)
}

type energyResponse struct {
	Emeter struct {
		GetRealtime *wireEnergy `json:"get_realtime"`
	} `json:"emeter"`
}

// Energy returns a real-time reading for one outlet ID.
func (s *Strip) Energy(ctx context.Context, outletID string) (Energy, error) {
	if strings.TrimSpace(outletID) == "" {
		return Energy{}, errors.New("tapo: outlet ID is required")
	}
	apiMode, err := s.ensureAPIMode(ctx)
	if err != nil {
		return Energy{}, err
	}
	if apiMode == deviceAPIModern {
		return s.modernEnergy(ctx, outletID)
	}
	request := childRequest(outletID, "emeter", "get_realtime", map[string]any{})
	var response energyResponse
	if err := s.query(ctx, request, &response); err != nil {
		return Energy{}, err
	}
	if response.Emeter.GetRealtime == nil {
		return Energy{}, errors.New("tapo: response is missing emeter.get_realtime")
	}
	wire := *response.Emeter.GetRealtime
	if err := checkDeviceError("emeter", "get_realtime", wire.errorFields); err != nil {
		return Energy{}, err
	}
	return wire.public(), nil
}

// SetOutlet switches one outlet on or off.
func (s *Strip) SetOutlet(ctx context.Context, outletID string, on bool) error {
	return s.SetOutlets(ctx, []string{outletID}, on)
}

// SetOutlets switches multiple outlets in one device command.
func (s *Strip) SetOutlets(ctx context.Context, outletIDs []string, on bool) error {
	if len(outletIDs) == 0 {
		return errors.New("tapo: at least one outlet ID is required")
	}
	for _, id := range outletIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("tapo: outlet IDs cannot be empty")
		}
	}
	apiMode, err := s.ensureAPIMode(ctx)
	if err != nil {
		return err
	}
	if apiMode == deviceAPIModern {
		return s.modernSetOutlets(ctx, outletIDs, on)
	}
	state := 0
	if on {
		state = 1
	}
	request := map[string]any{
		"context": map[string]any{"child_ids": outletIDs},
		"system":  map[string]any{"set_relay_state": map[string]int{"state": state}},
	}
	var response struct {
		System struct {
			SetRelayState *errorFields `json:"set_relay_state"`
		} `json:"system"`
	}
	if err := s.query(ctx, request, &response); err != nil {
		return err
	}
	if response.System.SetRelayState == nil {
		return errors.New("tapo: response is missing system.set_relay_state")
	}
	return checkDeviceError("system", "set_relay_state", *response.System.SetRelayState)
}

// SetOutletAt switches an outlet by its zero-based physical index. It first
// resolves the stable child ID from the strip.
func (s *Strip) SetOutletAt(ctx context.Context, index int, on bool) error {
	outlets, err := s.Outlets(ctx)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(outlets) {
		return fmt.Errorf("tapo: outlet index %d is out of range [0,%d)", index, len(outlets))
	}
	return s.SetOutlet(ctx, outlets[index].ID, on)
}

// RenameOutlet changes the alias shown for one outlet.
func (s *Strip) RenameOutlet(ctx context.Context, outletID, alias string) error {
	if strings.TrimSpace(outletID) == "" {
		return errors.New("tapo: outlet ID is required")
	}
	apiMode, err := s.ensureAPIMode(ctx)
	if err != nil {
		return err
	}
	if apiMode == deviceAPIModern {
		return s.modernRenameOutlet(ctx, outletID, alias)
	}
	request := childRequest(outletID, "system", "set_dev_alias", map[string]string{"alias": alias})
	var response struct {
		System struct {
			SetAlias *errorFields `json:"set_dev_alias"`
		} `json:"system"`
	}
	if err := s.query(ctx, request, &response); err != nil {
		return err
	}
	if response.System.SetAlias == nil {
		return errors.New("tapo: response is missing system.set_dev_alias")
	}
	return checkDeviceError("system", "set_dev_alias", *response.System.SetAlias)
}

// SetLED enables or disables the strip's status LED.
func (s *Strip) SetLED(ctx context.Context, enabled bool) error {
	apiMode, err := s.ensureAPIMode(ctx)
	if err != nil {
		return err
	}
	if apiMode == deviceAPIModern {
		return s.modernSetLED(ctx, enabled)
	}
	ledOff := 1
	if enabled {
		ledOff = 0
	}
	request := map[string]any{"system": map[string]any{"set_led_off": map[string]int{"off": ledOff}}}
	var response struct {
		System struct {
			SetLEDOff *errorFields `json:"set_led_off"`
		} `json:"system"`
	}
	if err := s.query(ctx, request, &response); err != nil {
		return err
	}
	if response.System.SetLEDOff == nil {
		return errors.New("tapo: response is missing system.set_led_off")
	}
	return checkDeviceError("system", "set_led_off", *response.System.SetLEDOff)
}

// DailyEnergy returns the daily usage counters for the outlet in date's month.
func (s *Strip) DailyEnergy(ctx context.Context, outletID string, date time.Time) ([]DailyUsage, error) {
	if strings.TrimSpace(outletID) == "" {
		return nil, errors.New("tapo: outlet ID is required")
	}
	apiMode, err := s.ensureAPIMode(ctx)
	if err != nil {
		return nil, err
	}
	if apiMode == deviceAPIModern {
		return s.modernDailyEnergy(ctx, outletID, date)
	}
	params := map[string]int{"month": int(date.Month()), "year": date.Year()}
	request := childRequest(outletID, "emeter", "get_daystat", params)
	var response struct {
		Emeter struct {
			GetDaystat *struct {
				errorFields
				Days []wireUsage `json:"day_list"`
			} `json:"get_daystat"`
		} `json:"emeter"`
	}
	if err := s.query(ctx, request, &response); err != nil {
		return nil, err
	}
	if response.Emeter.GetDaystat == nil {
		return nil, errors.New("tapo: response is missing emeter.get_daystat")
	}
	result := *response.Emeter.GetDaystat
	if err := checkDeviceError("emeter", "get_daystat", result.errorFields); err != nil {
		return nil, err
	}
	days := make([]DailyUsage, len(result.Days))
	for i, day := range result.Days {
		days[i] = DailyUsage{Day: day.Day, Energy: day.kWh()}
	}
	return days, nil
}

// MonthlyEnergy returns the monthly usage counters for an outlet and year.
func (s *Strip) MonthlyEnergy(ctx context.Context, outletID string, year int) ([]MonthlyUsage, error) {
	if strings.TrimSpace(outletID) == "" {
		return nil, errors.New("tapo: outlet ID is required")
	}
	if year < 2000 || year > 9999 {
		return nil, fmt.Errorf("tapo: invalid year %d", year)
	}
	apiMode, err := s.ensureAPIMode(ctx)
	if err != nil {
		return nil, err
	}
	if apiMode == deviceAPIModern {
		return s.modernMonthlyEnergy(ctx, outletID, year)
	}
	request := childRequest(outletID, "emeter", "get_monthstat", map[string]int{"year": year})
	var response struct {
		Emeter struct {
			GetMonthstat *struct {
				errorFields
				Months []wireUsage `json:"month_list"`
			} `json:"get_monthstat"`
		} `json:"emeter"`
	}
	if err := s.query(ctx, request, &response); err != nil {
		return nil, err
	}
	if response.Emeter.GetMonthstat == nil {
		return nil, errors.New("tapo: response is missing emeter.get_monthstat")
	}
	result := *response.Emeter.GetMonthstat
	if err := checkDeviceError("emeter", "get_monthstat", result.errorFields); err != nil {
		return nil, err
	}
	months := make([]MonthlyUsage, len(result.Months))
	for i, month := range result.Months {
		months[i] = MonthlyUsage{Month: time.Month(month.Month), Energy: month.kWh()}
	}
	return months, nil
}

func childRequest(outletID, module, command string, params any) map[string]any {
	return map[string]any{
		"context": map[string]any{"child_ids": []string{outletID}},
		module:    map[string]any{command: params},
	}
}
