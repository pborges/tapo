package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/pborges/tapo"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := store.add(stripConfig{Host: "10.0.0.5"}); err != nil || !added {
		t.Fatalf("add = %v, %v", added, err)
	}
	if added, _ := store.add(stripConfig{Host: "10.0.0.5"}); added {
		t.Fatal("duplicate host was added")
	}
	if _, err := store.add(stripConfig{Host: "10.0.0.6"}); err != nil {
		t.Fatal(err)
	}
	err = store.update("10.0.0.5", func(cfg *stripConfig) bool {
		cfg.Alias = "Rack"
		cfg.Outlets = []outletConfig{{Index: 0, ID: "a", Alias: "Pump"}}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := store.remove("10.0.0.6"); err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}

	reloaded, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	strips := reloaded.strips()
	if len(strips) != 1 || strips[0].Alias != "Rack" || len(strips[0].Outlets) != 1 || strips[0].Outlets[0].Alias != "Pump" {
		t.Fatalf("reloaded strips = %+v", strips)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	store, err := loadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(store.strips()) != 0 {
		t.Fatal("expected no strips")
	}
}

// newTestManager returns a manager whose single strip has a canned snapshot
// and no running poller.
func newTestManager(t *testing.T, lastSuccess time.Time) *manager {
	t.Helper()
	store, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := stripConfig{Host: "10.0.0.5", Alias: "Rack", Model: "HS300(US)"}
	if _, err := store.add(cfg); err != nil {
		t.Fatal(err)
	}
	m := newManager(context.Background(), store, nil, 10*time.Second)
	strip, err := tapo.New(cfg.Host, tapo.WithDisableLegacy())
	if err != nil {
		t.Fatal(err)
	}
	m.states[cfg.Host] = &stripState{
		host: cfg.Host, strip: strip, config: cfg, refresh: make(chan struct{}, 1), cancel: func() {},
		snapshot: &tapo.Snapshot{
			Device: tapo.DeviceInfo{Alias: "Rack", Model: "HS300(US)", DeviceID: "dev1", MAC: "AA:BB", RSSI: -52},
			Outlets: []tapo.Outlet{
				{Index: 0, ID: "a", Alias: "Pump", On: true, OnTime: 90 * time.Second,
					Energy: &tapo.Energy{Voltage: 120.5, Current: 0.5, Power: 60.25, Total: 1.5}},
				{Index: 1, ID: "b", Alias: "Light", On: false},
			},
			UpdatedAt: lastSuccess,
		},
		lastSuccess: lastSuccess,
		duration:    250 * time.Millisecond,
	}
	return m
}

func TestCollectorFresh(t *testing.T) {
	m := newTestManager(t, time.Now())
	expected := `
# HELP tapo_outlet_power_watts Real-time power draw.
# TYPE tapo_outlet_power_watts gauge
tapo_outlet_power_watts{host="10.0.0.5",model="HS300(US)",outlet="Pump",outlet_index="1",strip="Rack"} 60.25
# HELP tapo_outlet_on Whether the outlet relay is on (1) or off (0).
# TYPE tapo_outlet_on gauge
tapo_outlet_on{host="10.0.0.5",model="HS300(US)",outlet="Light",outlet_index="2",strip="Rack"} 0
tapo_outlet_on{host="10.0.0.5",model="HS300(US)",outlet="Pump",outlet_index="1",strip="Rack"} 1
# HELP tapo_outlet_energy_kwh_total Energy meter total reported by the device; may reset, so use increase().
# TYPE tapo_outlet_energy_kwh_total counter
tapo_outlet_energy_kwh_total{host="10.0.0.5",model="HS300(US)",outlet="Pump",outlet_index="1",strip="Rack"} 1.5
# HELP tapo_strip_up Whether the last poll of the strip succeeded and is recent (1) or not (0).
# TYPE tapo_strip_up gauge
tapo_strip_up{host="10.0.0.5",model="HS300(US)",strip="Rack"} 1
# HELP tapo_strip_rssi_dbm Wi-Fi signal strength reported by the strip.
# TYPE tapo_strip_rssi_dbm gauge
tapo_strip_rssi_dbm{host="10.0.0.5",model="HS300(US)",strip="Rack"} -52
`
	err := testutil.CollectAndCompare(collector{manager: m}, strings.NewReader(expected),
		"tapo_outlet_power_watts", "tapo_outlet_on", "tapo_outlet_energy_kwh_total", "tapo_strip_up", "tapo_strip_rssi_dbm")
	if err != nil {
		t.Fatal(err)
	}
	problems, err := testutil.CollectAndLint(collector{manager: m})
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Errorf("lint %s: %s", problem.Metric, problem.Text)
	}
}

func TestCollectorStaleWithholdsReadings(t *testing.T) {
	m := newTestManager(t, time.Now().Add(-time.Hour))
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector{manager: m})
	if n := testutil.CollectAndCount(collector{manager: m}, "tapo_outlet_power_watts"); n != 0 {
		t.Fatalf("stale strip exported %d power series", n)
	}
	expected := `
# HELP tapo_strip_up Whether the last poll of the strip succeeded and is recent (1) or not (0).
# TYPE tapo_strip_up gauge
tapo_strip_up{host="10.0.0.5",model="HS300(US)",strip="Rack"} 0
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "tapo_strip_up"); err != nil {
		t.Fatal(err)
	}
}

func TestListStripsAPI(t *testing.T) {
	m := newTestManager(t, time.Now())
	srv := httptest.NewServer((&server{manager: m, timeout: time.Second}).routes())
	defer srv.Close()

	response, err := http.Get(srv.URL + "/api/strips")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var strips []stripView
	if err := json.NewDecoder(response.Body).Decode(&strips); err != nil {
		t.Fatal(err)
	}
	if len(strips) != 1 {
		t.Fatalf("got %d strips", len(strips))
	}
	strip := strips[0]
	if !strip.Online || strip.Alias != "Rack" || strip.Power != 60.25 || len(strip.Outlets) != 2 {
		t.Fatalf("strip = %+v", strip)
	}
	if strip.Outlets[0].Index != 1 || !strip.Outlets[0].Metered || strip.Outlets[1].Metered {
		t.Fatalf("outlets = %+v", strip.Outlets)
	}
}

func TestStripManagementAPI(t *testing.T) {
	m := newTestManager(t, time.Now())
	srv := httptest.NewServer((&server{manager: m, timeout: time.Second}).routes())
	defer srv.Close()

	do := func(method, path, body string) (int, string) {
		t.Helper()
		request, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}

	if status, body := do("POST", "/api/strips", `{"host":"10.0.0.5"}`); status != http.StatusConflict {
		t.Fatalf("duplicate add = %d %s", status, body)
	}
	if status, body := do("POST", "/api/strips", `{"host":""}`); status != http.StatusBadRequest {
		t.Fatalf("empty add = %d %s", status, body)
	}
	if status, body := do("POST", "/api/strips/10.0.0.5/outlets/a/rename", `{"alias":"  "}`); status != http.StatusBadRequest {
		t.Fatalf("blank rename = %d %s", status, body)
	}
	if status, body := do("POST", "/api/strips/10.9.9.9/refresh", ``); status != http.StatusNotFound {
		t.Fatalf("unknown refresh = %d %s", status, body)
	}
	if status, body := do("POST", "/api/strips/10.0.0.5/refresh", ``); status != http.StatusAccepted {
		t.Fatalf("refresh = %d %s", status, body)
	}
	if status, body := do("DELETE", "/api/strips/10.0.0.5", ``); status != http.StatusNoContent {
		t.Fatalf("remove = %d %s", status, body)
	}
	if len(m.statuses()) != 0 || len(m.store.strips()) != 0 {
		t.Fatal("strip still present after removal")
	}
	if status, body := do("GET", "/", ``); status != http.StatusOK || !strings.Contains(body, "Tapo Forwarder") {
		t.Fatalf("index = %d", status)
	}
}

func TestParseOptions(t *testing.T) {
	t.Setenv("TAPO_USERNAME", "")
	t.Setenv("TAPO_PASSWORD", "")
	t.Setenv("TAPO_CONFIG", "/data/tapo.json")
	opts, err := parseOptions([]string{"-interval", "15s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.configPath != "/data/tapo.json" || opts.interval != 15*time.Second || opts.listen != ":9105" {
		t.Fatalf("opts = %+v", opts)
	}
	if _, err := parseOptions([]string{"-username", "a@b.c"}, io.Discard); err == nil {
		t.Fatal("expected error for username without password")
	}
	if _, err := parseOptions([]string{"-interval", "100ms"}, io.Discard); err == nil {
		t.Fatal("expected error for short interval")
	}
}
