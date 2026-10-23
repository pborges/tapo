package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/pborges/tapo"
)

//go:embed index.html
var indexHTML []byte

const maxAliasRunes = 40

type server struct {
	manager *manager
	subnet  string // default discovery subnet shown in the UI
	timeout time.Duration
}

func (s *server) routes() http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector{manager: s.manager})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("GET /api/strips", s.listStrips)
	mux.HandleFunc("POST /api/scan", s.scan)
	mux.HandleFunc("POST /api/strips", s.addStrip)
	mux.HandleFunc("DELETE /api/strips/{host}", s.removeStrip)
	mux.HandleFunc("POST /api/strips/{host}/refresh", s.refreshStrip)
	mux.HandleFunc("POST /api/strips/{host}/rename", s.renameStrip)
	mux.HandleFunc("POST /api/strips/{host}/outlets/{id}/state", s.setOutlet)
	mux.HandleFunc("POST /api/strips/{host}/outlets/{id}/rename", s.renameOutlet)
	return mux
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"subnet": s.subnet, "interval_s": s.manager.interval.Seconds(),
	})
}

type stripView struct {
	Host        string       `json:"host"`
	Alias       string       `json:"alias"`
	Model       string       `json:"model"`
	Online      bool         `json:"online"`
	Error       string       `json:"error,omitempty"`
	RSSI        int          `json:"rssi"`
	LastSuccess *time.Time   `json:"last_success,omitempty"`
	Power       float64      `json:"power_w"`
	Outlets     []outletView `json:"outlets"`
}

type outletView struct {
	Index   int     `json:"index"`
	ID      string  `json:"id"`
	Alias   string  `json:"alias"`
	On      bool    `json:"on"`
	OnTime  float64 `json:"on_time_s"`
	Metered bool    `json:"metered"`
	Power   float64 `json:"power_w"`
	Voltage float64 `json:"voltage_v"`
	Current float64 `json:"current_a"`
	Total   float64 `json:"total_kwh"`
}

func (s *server) listStrips(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	statuses := s.manager.statuses()
	views := make([]stripView, 0, len(statuses))
	for _, status := range statuses {
		view := stripView{
			Host: status.Host, Alias: status.Config.Alias, Model: status.Config.Model,
			Online: s.manager.fresh(status, now), Outlets: []outletView{},
		}
		if status.Err != nil {
			view.Error = status.Err.Error()
		}
		if !status.LastSuccess.IsZero() {
			lastSuccess := status.LastSuccess
			view.LastSuccess = &lastSuccess
		}
		if status.Snapshot == nil {
			// Never reached: show the outlets remembered in the config.
			for _, outlet := range status.Config.Outlets {
				view.Outlets = append(view.Outlets, outletView{Index: outlet.Index + 1, ID: outlet.ID, Alias: outlet.Alias})
			}
			views = append(views, view)
			continue
		}
		device := status.Snapshot.Device
		view.Alias, view.Model, view.RSSI = device.Alias, device.Model, device.RSSI
		for _, outlet := range status.Snapshot.Outlets {
			item := outletView{
				Index: outlet.Index + 1, ID: outlet.ID, Alias: outlet.Alias,
				On: outlet.On, OnTime: outlet.OnTime.Seconds(),
			}
			if outlet.Energy != nil {
				item.Metered = true
				item.Power, item.Voltage, item.Current, item.Total = outlet.Energy.Power, outlet.Energy.Voltage, outlet.Energy.Current, outlet.Energy.Total
				view.Power += outlet.Energy.Power
			}
			view.Outlets = append(view.Outlets, item)
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}

type scanResult struct {
	Host     string `json:"host"`
	HTTPPort int    `json:"http_port"`
	Alias    string `json:"alias"`
	Model    string `json:"model"`
	MAC      string `json:"mac"`
	Added    bool   `json:"added"`
}

func (s *server) scan(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Subnet string `json:"subnet"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	subnet := strings.TrimSpace(request.Subnet)
	var subnets []string
	if subnet != "" {
		subnets = append(subnets, subnet)
	}
	devices, err := tapo.Discover(r.Context(), s.timeout, subnets...)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	added := s.manager.hosts()
	results := []scanResult{}
	for _, device := range devices {
		if !supportedModel(device.Device.Model) {
			continue
		}
		results = append(results, scanResult{
			// device.Port is the UDP port the discovery reply came from, not
			// the legacy protocol port, so it is deliberately not offered.
			Host: device.Host, HTTPPort: device.HTTPPort,
			Alias: device.Device.Alias, Model: device.Device.Model, MAC: device.Device.MAC,
			Added: added[device.Host],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"subnet": subnet, "devices": results})
}

func supportedModel(model string) bool {
	model = strings.ToUpper(model)
	return strings.HasPrefix(model, "HS300") || strings.HasPrefix(model, "P316M")
}

func (s *server) addStrip(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		HTTPPort int    `json:"http_port"`
		Alias    string `json:"alias"`
		Model    string `json:"model"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	host := strings.TrimSpace(request.Host)
	if host == "" {
		writeError(w, http.StatusBadRequest, errors.New("host is required"))
		return
	}
	added, err := s.manager.add(stripConfig{
		Host: host, Port: request.Port, HTTPPort: request.HTTPPort, Alias: request.Alias, Model: request.Model,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !added {
		writeError(w, http.StatusConflict, errors.New(host+" is already configured"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"host": host})
}

func (s *server) removeStrip(w http.ResponseWriter, r *http.Request) {
	removed, err := s.manager.remove(r.PathValue("host"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, errUnknownStrip)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) refreshStrip(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.requestRefresh(r.PathValue("host")); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) renameStrip(w http.ResponseWriter, r *http.Request) {
	alias, ok := readAlias(w, r)
	if !ok {
		return
	}
	s.command(w, r, func(ctx context.Context, strip *tapo.Strip) error {
		return strip.RenameDevice(ctx, alias)
	})
}

func (s *server) setOutlet(w http.ResponseWriter, r *http.Request) {
	var request struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	id := r.PathValue("id")
	s.command(w, r, func(ctx context.Context, strip *tapo.Strip) error {
		return strip.SetOutlet(ctx, id, request.On)
	})
}

func (s *server) renameOutlet(w http.ResponseWriter, r *http.Request) {
	alias, ok := readAlias(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	s.command(w, r, func(ctx context.Context, strip *tapo.Strip) error {
		return strip.RenameOutlet(ctx, id, alias)
	})
}

// command runs fn against the strip named in the path, then has the poller
// refresh so the UI, config, and metrics pick up the change immediately.
func (s *server) command(w http.ResponseWriter, r *http.Request, fn func(context.Context, *tapo.Strip) error) {
	host := r.PathValue("host")
	strip, err := s.manager.client(host)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := fn(r.Context(), strip); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	s.manager.requestRefresh(host)
	w.WriteHeader(http.StatusNoContent)
}

func readAlias(w http.ResponseWriter, r *http.Request) (string, bool) {
	var request struct {
		Alias string `json:"alias"`
	}
	if !readJSON(w, r, &request) {
		return "", false
	}
	alias := strings.TrimSpace(request.Alias)
	if alias == "" || len([]rune(alias)) > maxAliasRunes {
		writeError(w, http.StatusBadRequest, errors.New("alias must be 1–40 characters"))
		return "", false
	}
	return alias, true
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
