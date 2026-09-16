package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marstid/goznp/pkg/adapter"
	"github.com/marstid/goznp/pkg/zcl"
	"github.com/marstid/goznp/pkg/znp"
)

const (
	interviewTimeout = 12 * time.Second
	commandTimeout   = 8 * time.Second
	readTimeout      = 3 * time.Second
)

// Device is the JSON-facing snapshot used by the UI.
type Device struct {
	IEEE         string     `json:"ieee"`
	NwkAddr      uint16     `json:"nwk_addr"`
	Name         string     `json:"name"`
	Kind         Kind       `json:"kind"`
	Manufacturer string     `json:"manufacturer,omitempty"`
	Model        string     `json:"model,omitempty"`
	Endpoint     uint8      `json:"endpoint"`
	Reachable    bool       `json:"reachable"`
	On           *bool      `json:"on,omitempty"`
	Brightness   *uint8     `json:"brightness,omitempty"` // 0-100
	Occupancy    *bool      `json:"occupancy,omitempty"`
	LastClick    string     `json:"last_click,omitempty"`
	ClickSeq     uint64     `json:"click_seq,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	LastSeenMs   int64      `json:"last_seen_ms,omitempty"`
	Error        string     `json:"error,omitempty"`
	HasOnOff     bool       `json:"has_on_off"`
	HasLevel     bool       `json:"has_level"`
	HasOccupancy bool       `json:"has_occupancy"`
	Clusters     []string   `json:"clusters,omitempty"`
	Endpoints    []Endpoint `json:"endpoints,omitempty"`
	UserLabel    bool       `json:"user_label,omitempty"`
	New          bool       `json:"new,omitempty"`
}

// Endpoint is a compact interview snapshot so unknown devices stay inspectable.
type Endpoint struct {
	ID  uint8    `json:"id"`
	In  []string `json:"in,omitempty"`
	Out []string `json:"out,omitempty"`
}

type cacheFile struct {
	Devices   map[string]Device `json:"devices"`
	Forgotten []string          `json:"forgotten,omitempty"`
}

// RemovedEvent is broadcast after a device is dropped from the hub.
type RemovedEvent struct {
	IEEE string `json:"ieee"`
	Name string `json:"name"`
}

// RenamedEvent is broadcast when the MQTT/UI friendly name changes.
type RenamedEvent struct {
	IEEE string `json:"ieee"`
	From string `json:"from"`
	To   string `json:"to"`
}

// Event is pushed to SSE subscribers.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type subscriber chan Event

// Hub talks to the coordinator and keeps UI state.
type Hub struct {
	log         *slog.Logger
	dataDir     string
	adapter     *adapter.Adapter
	radio       sync.Mutex // serializes ZNP; waiters steal unmatched frames
	mu          sync.RWMutex
	devices     map[string]*Device
	forgotten   map[string]struct{}
	presses     map[string]*pressWatch
	pressMu     sync.Mutex
	incoming    chan *znp.IncomingMessage
	bound       map[string]struct{}
	boundMu     sync.Mutex
	motionReady map[string]struct{}
	coordIEEE   [8]byte
	subs        map[subscriber]struct{}
	permitUntil time.Time
	netInfo     *adapter.NetworkInfo
	netAt       time.Time
}

type pressWatch struct {
	timer *time.Timer
	held  bool
	up    bool // released, waiting to confirm single vs multi-click
}

func New(log *slog.Logger, dataDir string) *Hub {
	return &Hub{
		log:         log,
		dataDir:     dataDir,
		devices:     make(map[string]*Device),
		forgotten:   make(map[string]struct{}),
		presses:     make(map[string]*pressWatch),
		bound:       make(map[string]struct{}),
		motionReady: make(map[string]struct{}),
		subs:        make(map[subscriber]struct{}),
	}
}

func (h *Hub) Open(ctx context.Context, serialPort string) error {
	a := adapter.New(adapter.WithSerialPath(serialPort))
	if err := a.Open(ctx); err != nil {
		return fmt.Errorf("open adapter: %w", err)
	}
	h.adapter = a
	h.loadCache()
	a.OnDeviceEvent(func(ev adapter.DeviceEvent) {
		ieee := formatIEEE(ev.IEEEAddr)
		h.log.Info("device event", "type", ev.Type, "ieee", ieee)
		if ev.Type == adapter.DeviceEventJoined {
			h.unforget(ieee)
		}
		go func() {
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := h.Refresh(c); err != nil {
				h.log.Warn("refresh after event", "err", err)
			}
		}()
	})
	h.hookIncoming()
	if err := h.expandHAEndpoint(ctx); err != nil {
		h.log.Warn("coordinator onoff endpoint", "err", err)
	}
	h.radio.Lock()
	ieee, err := h.adapter.GetCoordinatorIEEE(ctx)
	h.radio.Unlock()
	if err != nil {
		h.log.Warn("coordinator ieee", "err", err)
	} else {
		h.coordIEEE = ieee
	}
	return h.Refresh(ctx)
}

func (h *Hub) Close() error {
	if h.adapter == nil {
		return nil
	}
	return h.adapter.Close()
}

func (h *Hub) Network(ctx context.Context) (*adapter.NetworkInfo, error) {
	h.mu.RLock()
	if h.netInfo != nil && time.Since(h.netAt) < 30*time.Second {
		info := h.netInfo
		h.mu.RUnlock()
		return info, nil
	}
	h.mu.RUnlock()

	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h.radio.Lock()
	info, err := h.adapter.GetNetworkInfo(c)
	h.radio.Unlock()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.netInfo = info
	h.netAt = time.Now()
	h.mu.Unlock()
	return info, nil
}

func (h *Hub) Devices() []Device {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]Device, 0, len(h.devices))
	for _, d := range h.devices {
		out = append(out, *d)
	}
	return out
}

func (h *Hub) Device(ieee string) (Device, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	d, ok := h.devices[compactIEEE(ieee)]
	if !ok {
		return Device{}, errNotFound
	}
	return *d, nil
}

func (h *Hub) DeviceByName(name string) (Device, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, d := range h.devices {
		if d.Name == name || d.IEEE == compactIEEE(name) {
			return *d, nil
		}
	}
	return Device{}, errNotFound
}

var (
	errNotFound     = errors.New("device not found")
	ErrUnavailable  = errors.New("unavailable")
	unavailableText = "unavailable"
)

func (h *Hub) Refresh(ctx context.Context) error {
	h.radio.Lock()
	list, err := h.adapter.GetDevices(ctx)
	h.radio.Unlock()
	if err != nil {
		return fmt.Errorf("get devices: %w", err)
	}

	h.mu.Lock()
	seen := make(map[string]struct{}, len(list))
	for _, raw := range list {
		ieee := formatIEEE(raw.IEEEAddr)
		if _, skip := h.forgotten[ieee]; skip {
			continue
		}
		seen[ieee] = struct{}{}
		d, ok := h.devices[ieee]
		if !ok {
			d = &Device{IEEE: ieee, Endpoint: 1, Kind: KindUnknown, Name: ieee}
			if _, known := lookupKnown(ieee); !known {
				d.New = true
			}
			h.devices[ieee] = d
			h.log.Info("new device", "ieee", ieee, "nwk", raw.NwkAddr)
		}
		d.NwkAddr = raw.NwkAddr
		if !d.UserLabel {
			if known, ok := lookupKnown(ieee); ok {
				d.Name = known.Name
				if d.Kind == "" || d.Kind == KindUnknown {
					d.Kind = known.Kind
				}
			} else if d.Name == "" {
				d.Name = ieee
				d.Kind = KindUnknown
			}
		}
	}
	for ieee := range h.devices {
		if _, ok := seen[ieee]; !ok {
			h.devices[ieee].Reachable = false
		}
	}
	snapshot := h.cloneLocked()
	h.mu.Unlock()
	h.saveCache()
	h.broadcast(Event{Type: "devices", Data: snapshot})
	return nil
}

func (h *Hub) interviewMissing() {
	h.mu.RLock()
	need := make([]Device, 0)
	for _, d := range h.devices {
		if d.Kind == KindSwitch || d.Kind == KindSensor || d.Kind == KindMotion {
			continue
		}
		if d.Manufacturer == "" && d.Model == "" && len(d.Clusters) == 0 {
			need = append(need, *d)
		}
	}
	h.mu.RUnlock()

	for _, d := range need {
		ctx, cancel := context.WithTimeout(context.Background(), interviewTimeout+time.Second)
		_ = h.Interview(ctx, d.IEEE)
		cancel()
	}
}

func (h *Hub) Interview(ctx context.Context, ieee string) error {
	d, err := h.Device(ieee)
	if err != nil {
		return err
	}
	addr, err := parseIEEE(ieee)
	if err != nil {
		return err
	}
	h.radio.Lock()
	result, err := h.adapter.InterviewDeviceWithAddr(ctx, d.NwkAddr, addr)
	h.radio.Unlock()
	h.mu.Lock()
	dev := h.devices[compactIEEE(ieee)]
	if dev == nil {
		h.mu.Unlock()
		return err
	}
	if err != nil {
		dev.Reachable = false
		dev.Error = displayRadioError(err)
		h.mu.Unlock()
		h.saveCache()
		h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
		return mapRadioError(err)
	}
	dev.Manufacturer = result.Manufacturer
	dev.Model = result.Model
	dev.Error = ""
	applyInterview(dev, result)
	h.mu.Unlock()

	h.readState(dev.IEEE)
	if dev.Kind == KindMotion || dev.HasOccupancy {
		h.maybeSetupMotion(mustDevice(h, ieee))
	}
	h.saveCache()
	h.broadcast(Event{Type: "device", Data: mustDevice(h, dev.IEEE)})
	return nil
}

func applyInterview(dev *Device, result *adapter.InterviewResult) {
	in := make([]uint16, 0)
	snaps := make([]Endpoint, 0, len(result.Endpoints))
	names := make([]string, 0)
	seen := map[string]struct{}{}
	for _, ep := range result.Endpoints {
		snap := Endpoint{ID: ep.Endpoint}
		for _, c := range ep.InClusters {
			in = append(in, c)
			n := clusterName(c)
			snap.In = append(snap.In, n)
			if _, ok := seen[n]; !ok {
				seen[n] = struct{}{}
				names = append(names, n)
			}
		}
		for _, c := range ep.OutClusters {
			n := clusterName(c)
			snap.Out = append(snap.Out, n)
		}
		snaps = append(snaps, snap)
	}
	dev.Endpoints = snaps
	dev.Clusters = names
	dev.HasOnOff = hasCluster(in, zcl.ClusterOnOff)
	dev.HasLevel = hasCluster(in, zcl.ClusterLevelControl)
	dev.HasOccupancy = hasCluster(in, zcl.ClusterOccupancySensing) || hasCluster(in, zcl.ClusterIASZone)
	if ep := result.FindEndpointWithCluster(uint16(zcl.ClusterOnOff)); ep != nil {
		dev.Endpoint = ep.Endpoint
	} else if ep := result.FindEndpointWithCluster(uint16(zcl.ClusterOccupancySensing)); ep != nil {
		dev.Endpoint = ep.Endpoint
	} else if ep := result.FindEndpointWithCluster(uint16(zcl.ClusterIASZone)); ep != nil {
		dev.Endpoint = ep.Endpoint
	} else if len(result.Endpoints) > 0 {
		dev.Endpoint = result.Endpoints[0].Endpoint
	}
	if !dev.UserLabel {
		if known, ok := lookupKnown(dev.IEEE); ok {
			dev.Name = known.Name
			dev.Kind = known.Kind
		} else {
			dev.Kind = inferKind(result.Manufacturer, result.Model, in)
			if result.Model != "" {
				dev.Name = strings.TrimSpace(result.Manufacturer + " " + result.Model)
			}
		}
	}
}

func (h *Hub) Update(ieee, name string, kind Kind) error {
	if kind != "" && !ValidKind(kind) {
		return fmt.Errorf("unknown kind %q", kind)
	}
	h.mu.Lock()
	dev := h.devices[compactIEEE(ieee)]
	if dev == nil {
		h.mu.Unlock()
		return errNotFound
	}
	oldName := dev.Name
	if name != "" {
		dev.Name = name
	}
	if kind != "" {
		dev.Kind = kind
	}
	dev.UserLabel = true
	dev.New = false
	newName := dev.Name
	h.mu.Unlock()
	h.saveCache()
	if name != "" && oldName != "" && oldName != newName {
		h.broadcast(Event{Type: "renamed", Data: RenamedEvent{
			IEEE: compactIEEE(ieee),
			From: oldName,
			To:   newName,
		}})
	}
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
	return nil
}

func (h *Hub) Remove(ctx context.Context, ieee string) error {
	d, err := h.Device(ieee)
	if err != nil {
		return err
	}
	addr, err := parseIEEE(ieee)
	if err != nil {
		return err
	}

	c, cancel := context.WithTimeout(ctx, commandTimeout)
	h.radio.Lock()
	leaveErr := h.adapter.RemoveDevice(c, d.NwkAddr, addr, false, false)
	h.radio.Unlock()
	cancel()
	if leaveErr != nil {
		h.log.Info("leave failed, force-removing", "ieee", d.IEEE, "err", leaveErr)
		c2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
		h.radio.Lock()
		forceErr := h.adapter.ForceRemoveDevice(c2, addr)
		h.radio.Unlock()
		cancel2()
		if forceErr != nil && !errors.Is(forceErr, adapter.ErrDeviceNotFound) {
			h.log.Warn("force remove", "ieee", d.IEEE, "err", forceErr)
		}
	}

	key := compactIEEE(ieee)
	h.mu.Lock()
	delete(h.devices, key)
	h.forgotten[key] = struct{}{}
	h.mu.Unlock()
	h.saveCache()
	h.broadcast(Event{Type: "removed", Data: RemovedEvent{IEEE: key, Name: d.Name}})
	return nil
}

func (h *Hub) unforget(ieee string) {
	h.mu.Lock()
	delete(h.forgotten, compactIEEE(ieee))
	h.mu.Unlock()
}

func (h *Hub) readState(ieee string) {
	d, err := h.Device(ieee)
	if err != nil {
		return
	}
	now := time.Now()
	if d.HasOnOff || d.Kind == KindLight || d.Kind == KindPlug {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		h.radio.Lock()
		on, err := h.adapter.GetOnOffState(ctx, d.NwkAddr, d.Endpoint)
		h.radio.Unlock()
		cancel()
		h.mu.Lock()
		if dev := h.devices[compactIEEE(ieee)]; dev != nil {
			if err != nil {
				dev.Reachable = false
			} else {
				dev.Reachable = true
				dev.On = &on
				dev.LastSeen = &now
			}
		}
		h.mu.Unlock()
	}
	if d.HasLevel || d.Kind == KindLight {
		c2, cancel2 := context.WithTimeout(context.Background(), readTimeout)
		h.radio.Lock()
		level, err := h.adapter.GetBrightness(c2, d.NwkAddr, d.Endpoint)
		h.radio.Unlock()
		cancel2()
		if err == nil {
			pct := uint8(float64(level) / 254 * 100)
			h.mu.Lock()
			if dev := h.devices[compactIEEE(ieee)]; dev != nil {
				dev.Brightness = &pct
				dev.Reachable = true
				dev.LastSeen = &now
			}
			h.mu.Unlock()
		}
	}
	if d.HasOccupancy || d.Kind == KindMotion {
		ep := endpointWith(d, "OccupancySensing")
		if ep == 0 {
			ep = endpointWith(d, "IASZone")
		}
		if ep == 0 {
			ep = d.Endpoint
		}
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		occ, err := h.readOccupancy(ctx, d.NwkAddr, ep, d)
		cancel()
		if err == nil {
			h.mu.Lock()
			if dev := h.devices[compactIEEE(ieee)]; dev != nil {
				dev.Occupancy = &occ
				dev.Reachable = true
				dev.LastSeen = &now
			}
			h.mu.Unlock()
		}
	}
}

func endpointWith(d Device, cluster string) uint8 {
	for _, ep := range d.Endpoints {
		for _, n := range ep.In {
			if n == cluster {
				return ep.ID
			}
		}
	}
	return 0
}

func (h *Hub) readOccupancy(ctx context.Context, nwk uint16, ep uint8, d Device) (bool, error) {
	h.radio.Lock()
	defer h.radio.Unlock()
	if occEP := endpointWith(d, "OccupancySensing"); occEP != 0 {
		results, err := h.adapter.ReadAttributes(ctx, nwk, occEP, zcl.ClusterOccupancySensing, zcl.AttrOccupancyValue)
		if err == nil && len(results) > 0 && results[0].Status == zcl.StatusSuccess {
			if occ, ok := occupancyFromValue(results[0].Value); ok {
				return occ, nil
			}
		}
	}
	if iasEP := endpointWith(d, "IASZone"); iasEP != 0 {
		st, err := h.adapter.GetIASZoneStatus(ctx, nwk, iasEP)
		if err != nil {
			return false, err
		}
		return st.Alarm1 || st.Alarm2, nil
	}
	return false, fmt.Errorf("no occupancy cluster")
}

func occupancyFromValue(v any) (bool, bool) {
	switch x := v.(type) {
	case uint8:
		return x&1 != 0, true
	case uint16:
		return x&1 != 0, true
	case int:
		return x&1 != 0, true
	case bool:
		return x, true
	default:
		return false, false
	}
}

func (h *Hub) Turn(ctx context.Context, ieee string, on bool) error {
	d, err := h.Device(ieee)
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	h.radio.Lock()
	if on {
		err = h.adapter.TurnOn(c, d.NwkAddr, d.Endpoint)
	} else {
		err = h.adapter.TurnOff(c, d.NwkAddr, d.Endpoint)
	}
	h.radio.Unlock()
	if err != nil {
		return h.noteUnavailable(ieee, err)
	}
	h.mu.Lock()
	if dev := h.devices[compactIEEE(ieee)]; dev != nil {
		dev.On = &on
		dev.Reachable = true
		dev.Error = ""
		now := time.Now()
		dev.LastSeen = &now
	}
	h.mu.Unlock()
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
	return nil
}

func (h *Hub) SetBrightness(ctx context.Context, ieee string, percent uint8) error {
	if percent > 100 {
		percent = 100
	}
	d, err := h.Device(ieee)
	if err != nil {
		return err
	}
	level := uint8(float64(percent) / 100 * 254)
	c, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	h.radio.Lock()
	err = h.adapter.SetBrightness(c, d.NwkAddr, d.Endpoint, level, 5)
	h.radio.Unlock()
	if err != nil {
		return h.noteUnavailable(ieee, err)
	}
	on := percent > 0
	h.mu.Lock()
	if dev := h.devices[compactIEEE(ieee)]; dev != nil {
		dev.Brightness = &percent
		dev.On = &on
		dev.Reachable = true
		dev.Error = ""
		now := time.Now()
		dev.LastSeen = &now
	}
	h.mu.Unlock()
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
	return nil
}

func (h *Hub) PermitJoin(ctx context.Context, seconds uint8) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h.radio.Lock()
	err := h.adapter.PermitJoin(c, seconds)
	h.radio.Unlock()
	if err != nil {
		return err
	}
	h.mu.Lock()
	if seconds == 0 {
		h.permitUntil = time.Time{}
	} else {
		h.permitUntil = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	until := h.permitUntil
	h.mu.Unlock()
	h.broadcast(Event{Type: "permit_join", Data: map[string]any{
		"seconds": seconds,
		"until":   until,
	}})
	return nil
}

func (h *Hub) PermitUntil() time.Time {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.permitUntil
}

func (h *Hub) Subscribe() (subscriber, func()) {
	ch := make(subscriber, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
		close(ch)
	}
}

func (h *Hub) broadcast(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			h.log.Warn("event dropped", "type", ev.Type)
		}
	}
}

func (h *Hub) cloneLocked() []Device {
	out := make([]Device, 0, len(h.devices))
	for _, d := range h.devices {
		out = append(out, *d)
	}
	return out
}

func mustDevice(h *Hub, ieee string) Device {
	d, _ := h.Device(ieee)
	return d
}

func (h *Hub) cachePath() string {
	return filepath.Join(h.dataDir, "devices.json")
}

func (h *Hub) loadCache() {
	raw, err := os.ReadFile(h.cachePath())
	if err != nil {
		return
	}
	var cf cacheFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		h.log.Warn("cache decode", "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ieee, d := range cf.Devices {
		cp := d
		h.devices[compactIEEE(ieee)] = &cp
	}
	for _, ieee := range cf.Forgotten {
		h.forgotten[compactIEEE(ieee)] = struct{}{}
	}
}

func (h *Hub) saveCache() {
	if h.dataDir == "" {
		return
	}
	_ = os.MkdirAll(h.dataDir, 0o755)
	h.mu.RLock()
	cf := cacheFile{
		Devices:   make(map[string]Device, len(h.devices)),
		Forgotten: make([]string, 0, len(h.forgotten)),
	}
	for k, d := range h.devices {
		cf.Devices[k] = *d
	}
	for k := range h.forgotten {
		cf.Forgotten = append(cf.Forgotten, k)
	}
	h.mu.RUnlock()
	raw, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(h.cachePath(), raw, 0o644)
}

func IsNotFound(err error) bool {
	return errors.Is(err, errNotFound)
}

func IsUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable)
}

func (h *Hub) noteUnavailable(ieee string, err error) error {
	h.log.Info("device unavailable", "ieee", compactIEEE(ieee), "err", err)
	h.mu.Lock()
	if dev := h.devices[compactIEEE(ieee)]; dev != nil {
		dev.Reachable = false
		dev.Error = displayRadioError(err)
	}
	h.mu.Unlock()
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
	return mapRadioError(err)
}

func displayRadioError(err error) string {
	if isUnreachable(err) {
		return unavailableText
	}
	return err.Error()
}

func mapRadioError(err error) error {
	if isUnreachable(err) {
		return ErrUnavailable
	}
	return err
}

func isUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "timeout") || strings.Contains(s, "deadline") {
		return true
	}
	for _, code := range []string{"0xb7", "0xe9", "0xe4", "0xcd", "0xa7", "0xf0", "0xe1"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}
