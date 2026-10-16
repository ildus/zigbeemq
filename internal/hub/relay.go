package hub

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/marstid/goznp/pkg/zcl"
	"github.com/marstid/goznp/pkg/znp"
)

const (
	clusterEwelink zcl.ClusterID = 0xFC11
	coolkitManuf   uint16        = 0x1286
	relayWait                    = 8 * time.Second
)

const (
	attrNetworkLed      zcl.AttributeID = 0x0001
	attrRadioPower      zcl.AttributeID = 0x0012
	attrDelayedState    zcl.AttributeID = 0x0014
	attrDelayedTime     zcl.AttributeID = 0x0015
	attrExternalTrigger zcl.AttributeID = 0x0016
	attrDetachRelay     zcl.AttributeID = 0x0017
	ewelinkProtocolCmd  uint8           = 0x01
)

// Relay is the SONOFF ZBMINI-R2 config zigbee2mqtt exposes.
type Relay struct {
	PowerOnBehavior     string   `json:"power_on_behavior,omitempty"`
	NetworkIndicator    *bool    `json:"network_indicator,omitempty"`
	TurboMode           *bool    `json:"turbo_mode,omitempty"`
	DelayedPowerOnState *bool    `json:"delayed_power_on_state,omitempty"`
	DelayedPowerOnTime  *float64 `json:"delayed_power_on_time,omitempty"`
	DetachRelayMode     *bool    `json:"detach_relay_mode,omitempty"`
	ExternalTriggerMode string   `json:"external_trigger_mode,omitempty"`
}

// Inching is a one-shot eWeLink pulse. The device does not report it back.
type Inching struct {
	Control string  `json:"inching_control"`
	Time    float64 `json:"inching_time"`
	Mode    string  `json:"inching_mode"`
}

// RelayPatch is a partial update. Nil fields are left unchanged.
type RelayPatch struct {
	PowerOnBehavior     *string  `json:"power_on_behavior,omitempty"`
	NetworkIndicator    *bool    `json:"network_indicator,omitempty"`
	TurboMode           *bool    `json:"turbo_mode,omitempty"`
	DelayedPowerOnState *bool    `json:"delayed_power_on_state,omitempty"`
	DelayedPowerOnTime  *float64 `json:"delayed_power_on_time,omitempty"`
	DetachRelayMode     *bool    `json:"detach_relay_mode,omitempty"`
	ExternalTriggerMode *string  `json:"external_trigger_mode,omitempty"`
	Inching             *Inching `json:"inching_control_set,omitempty"`
}

var (
	errNotRelay   = errors.New("not a ZBMINIR2")
	errRelayInput = errors.New("bad relay setting")
)

func IsNotRelay(err error) bool   { return errors.Is(err, errNotRelay) }
func IsRelayInput(err error) bool { return errors.Is(err, errRelayInput) }

func IsSonoffMini(d Device) bool {
	switch d.Model {
	case "ZBMINIR2", "MINI-ZBD":
		return true
	default:
		return false
	}
}

func (p RelayPatch) Empty() bool {
	return p.PowerOnBehavior == nil && p.NetworkIndicator == nil && p.TurboMode == nil &&
		p.DelayedPowerOnState == nil && p.DelayedPowerOnTime == nil && p.DetachRelayMode == nil &&
		p.ExternalTriggerMode == nil && p.Inching == nil
}

func (h *Hub) scheduleRelaySetup() {
	for _, d := range h.Devices() {
		if IsSonoffMini(d) && !d.Archived {
			go h.setupRelay(d.IEEE)
		}
	}
}

func (h *Hub) setupRelay(ieee string) {
	key := compactIEEE(ieee)
	h.relayMu.Lock()
	if _, ok := h.relayBusy[key]; ok {
		h.relayMu.Unlock()
		return
	}
	h.relayBusy[key] = struct{}{}
	h.relayMu.Unlock()
	defer func() {
		h.relayMu.Lock()
		delete(h.relayBusy, key)
		h.relayMu.Unlock()
	}()

	d, err := h.Device(key)
	if err != nil || !IsSonoffMini(d) || d.Archived {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	h.bindRelay(ctx, d)
	if _, err := h.ReadRelay(ctx, key); err != nil {
		h.log.Info("relay read", "ieee", key, "err", err)
	}
	h.reportRelayOnOff(ctx, d)
}

func (h *Hub) bindRelay(ctx context.Context, d Device) {
	if h.coordIEEE == [8]byte{} {
		return
	}
	addr, err := parseIEEE(d.IEEE)
	if err != nil {
		return
	}
	ep := relayEndpoint(d)
	for _, cluster := range []uint16{uint16(zcl.ClusterOnOff), uint16(clusterEwelink)} {
		c, cancel := context.WithTimeout(ctx, commandTimeout)
		h.radio.Lock()
		err = h.adapter.Bind(c, addr, d.NwkAddr, ep, cluster, h.coordIEEE, 1)
		h.radio.Unlock()
		cancel()
		if err != nil {
			h.log.Info("relay bind", "ieee", d.IEEE, "cluster", fmt.Sprintf("0x%04X", cluster), "err", err)
		}
	}
}

func (h *Hub) reportRelayOnOff(ctx context.Context, d Device) {
	frame, err := zcl.BuildConfigureReportingRequest(0, []zcl.ReportingConfig{{
		AttributeID: zcl.AttrOnOff,
		DataType:    zcl.TypeBoolean,
		MinInterval: 1,
		MaxInterval: 1800,
	}})
	if err != nil {
		h.log.Info("relay report", "ieee", d.IEEE, "err", err)
		return
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err = h.relayExchange(c, d.NwkAddr, relayEndpoint(d), zcl.ClusterOnOff, frame, uint8(zcl.CmdConfigureReportingResp)); err != nil {
		h.log.Info("relay report", "ieee", d.IEEE, "err", err)
	}
}

func (h *Hub) ReadRelay(ctx context.Context, ieee string) (Relay, error) {
	d, err := h.Device(ieee)
	if err != nil {
		return Relay{}, err
	}
	if !IsSonoffMini(d) {
		return Relay{}, errNotRelay
	}
	if h.adapter == nil {
		return Relay{}, fmt.Errorf("adapter closed")
	}
	next := Relay{}
	if d.Relay != nil {
		next = *d.Relay
	}
	ep := relayEndpoint(d)
	var first error
	startup, err := h.readAttrs(ctx, d.NwkAddr, ep, zcl.ClusterOnOff, 0, zcl.AttrStartUpOnOff)
	if err != nil {
		first = err
		h.log.Info("relay attr", "ieee", d.IEEE, "attr", "power_on_behavior", "err", err)
	}
	var ewelink []zcl.ReadAttributeResult
	for _, id := range []zcl.AttributeID{attrNetworkLed, attrRadioPower, attrDelayedState, attrDelayedTime, attrExternalTrigger, attrDetachRelay} {
		res, err := h.readAttrs(ctx, d.NwkAddr, ep, clusterEwelink, 0, id)
		if err != nil || !relayAttrsOK(res) {
			if err != nil && first == nil {
				first = err
			}
			if err != nil {
				h.log.Info("relay attr", "ieee", d.IEEE, "id", fmt.Sprintf("0x%04X", uint16(id)), "err", err)
			}
			again, err2 := h.readAttrs(ctx, d.NwkAddr, ep, clusterEwelink, coolkitManuf, id)
			if err2 == nil && relayAttrsOK(again) {
				res = again
			} else if err2 != nil {
				h.log.Info("relay attr", "ieee", d.IEEE, "id", fmt.Sprintf("0x%04X", uint16(id)), "manuf", fmt.Sprintf("0x%04X", coolkitManuf), "err", err2)
				if first == nil {
					first = err2
				}
				continue
			}
		}
		ewelink = append(ewelink, res...)
	}
	for _, r := range append(startup, ewelink...) {
		if r.Status != zcl.StatusSuccess {
			h.log.Info("relay attr", "ieee", d.IEEE, "id", fmt.Sprintf("0x%04X", uint16(r.AttributeID)), "status", fmt.Sprintf("0x%02X", r.Status))
		}
	}
	gotStartup := applyRelayRead(&next, startup)
	gotEwelink := applyRelayRead(&next, ewelink)
	got := gotStartup || gotEwelink
	if !got {
		if first == nil {
			first = fmt.Errorf("relay settings unread")
		}
		if isUnreachable(first) {
			return Relay{}, h.noteUnavailable(d.IEEE, first)
		}
		return Relay{}, first
	}
	h.storeRelay(d.IEEE, next)
	return next, nil
}

func relayAttrsOK(results []zcl.ReadAttributeResult) bool {
	for _, r := range results {
		if r.Status == zcl.StatusSuccess {
			return true
		}
	}
	return false
}

func applyRelayRead(next *Relay, results []zcl.ReadAttributeResult) bool {
	got := false
	for _, r := range results {
		if r.Status != zcl.StatusSuccess {
			continue
		}
		switch r.AttributeID {
		case zcl.AttrStartUpOnOff:
			if name, ok := powerOnName(asU8(r.Value)); ok {
				next.PowerOnBehavior = name
				got = true
			}
		case attrNetworkLed:
			v := asBool(r.Value)
			next.NetworkIndicator = &v
			got = true
		case attrRadioPower:
			if on, ok := turboOn(asI16(r.Value)); ok {
				next.TurboMode = &on
				got = true
			}
		case attrDelayedState:
			v := asBool(r.Value)
			next.DelayedPowerOnState = &v
			got = true
		case attrDelayedTime:
			sec := float64(asU16(r.Value)) / 2
			next.DelayedPowerOnTime = &sec
			got = true
		case attrExternalTrigger:
			if name, ok := triggerName(asU8(r.Value)); ok {
				next.ExternalTriggerMode = name
				got = true
			}
		case attrDetachRelay:
			v := asBool(r.Value)
			next.DetachRelayMode = &v
			got = true
		}
	}
	return got
}

func (h *Hub) ApplyRelay(ctx context.Context, ieee string, p RelayPatch) (Relay, error) {
	d, err := h.Device(ieee)
	if err != nil {
		return Relay{}, err
	}
	if !IsSonoffMini(d) {
		return Relay{}, errNotRelay
	}
	if p.Empty() {
		return Relay{}, fmt.Errorf("%w: empty", errRelayInput)
	}
	var startup uint8
	var trigger uint8
	var delay uint16
	var inch []byte
	if p.PowerOnBehavior != nil {
		startup, err = powerOnValue(*p.PowerOnBehavior)
		if err != nil {
			return Relay{}, err
		}
	}
	if p.ExternalTriggerMode != nil {
		trigger, err = triggerValue(*p.ExternalTriggerMode)
		if err != nil {
			return Relay{}, err
		}
	}
	if p.DelayedPowerOnTime != nil {
		delay, err = delayRaw(*p.DelayedPowerOnTime)
		if err != nil {
			return Relay{}, err
		}
	}
	if p.Inching != nil {
		inch, err = inchingPayload(p.Inching.Control, p.Inching.Mode, p.Inching.Time)
		if err != nil {
			return Relay{}, err
		}
	}
	if h.adapter == nil {
		return Relay{}, fmt.Errorf("adapter closed")
	}

	ep := relayEndpoint(d)
	if p.PowerOnBehavior != nil {
		if err := h.writeAttrs(ctx, d.NwkAddr, ep, zcl.ClusterOnOff, 0, map[zcl.AttributeID]zcl.AttributeValue{
			zcl.AttrStartUpOnOff: {Type: zcl.TypeEnum8, Value: startup},
		}); err != nil {
			return Relay{}, h.relayRadio(d.IEEE, err)
		}
	}
	ewelinkVals := map[zcl.AttributeID]zcl.AttributeValue{}
	if p.NetworkIndicator != nil {
		ewelinkVals[attrNetworkLed] = zcl.AttributeValue{Type: zcl.TypeBoolean, Value: *p.NetworkIndicator}
	}
	if p.TurboMode != nil {
		ewelinkVals[attrRadioPower] = zcl.AttributeValue{Type: zcl.TypeInt16, Value: turboValue(*p.TurboMode)}
	}
	if p.DelayedPowerOnState != nil {
		ewelinkVals[attrDelayedState] = zcl.AttributeValue{Type: zcl.TypeBoolean, Value: *p.DelayedPowerOnState}
	}
	if p.DelayedPowerOnTime != nil {
		ewelinkVals[attrDelayedTime] = zcl.AttributeValue{Type: zcl.TypeUint16, Value: delay}
	}
	if p.ExternalTriggerMode != nil {
		ewelinkVals[attrExternalTrigger] = zcl.AttributeValue{Type: zcl.TypeUint8, Value: trigger}
	}
	if p.DetachRelayMode != nil {
		ewelinkVals[attrDetachRelay] = zcl.AttributeValue{Type: zcl.TypeBoolean, Value: *p.DetachRelayMode}
	}
	if len(ewelinkVals) > 0 {
		if err := h.writeAttrs(ctx, d.NwkAddr, ep, clusterEwelink, 0, ewelinkVals); err != nil {
			if err2 := h.writeAttrs(ctx, d.NwkAddr, ep, clusterEwelink, coolkitManuf, ewelinkVals); err2 != nil {
				return Relay{}, h.relayRadio(d.IEEE, err2)
			}
		}
	}
	if inch != nil {
		if err := h.sendEwelink(ctx, d.NwkAddr, ep, inch); err != nil {
			return Relay{}, h.relayRadio(d.IEEE, err)
		}
	}
	return h.ReadRelay(ctx, d.IEEE)
}

func (h *Hub) relayRadio(ieee string, err error) error {
	if isUnreachable(err) {
		return h.noteUnavailable(ieee, err)
	}
	return err
}

func (h *Hub) readAttrs(ctx context.Context, nwk uint16, ep uint8, cluster zcl.ClusterID, manuf uint16, attrs ...zcl.AttributeID) ([]zcl.ReadAttributeResult, error) {
	frame := zcl.BuildReadAttributesRequest(0, attrs...)
	if manuf != 0 {
		frame.Control.ManufacturerSpecific = true
		frame.ManufCode = manuf
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := h.relayExchange(c, nwk, ep, cluster, frame, uint8(zcl.CmdReadAttributesResponse))
	if err != nil {
		return nil, err
	}
	return zcl.ParseReadAttributesResponse(resp.Payload)
}

func (h *Hub) writeAttrs(ctx context.Context, nwk uint16, ep uint8, cluster zcl.ClusterID, manuf uint16, values map[zcl.AttributeID]zcl.AttributeValue) error {
	frame, err := zcl.BuildWriteAttributesRequest(0, values)
	if err != nil {
		return err
	}
	if manuf != 0 {
		frame.Control.ManufacturerSpecific = true
		frame.ManufCode = manuf
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := h.relayExchange(c, nwk, ep, cluster, frame, uint8(zcl.CmdWriteAttributesResponse))
	if err != nil {
		return err
	}
	results, err := zcl.ParseWriteAttributesResponse(resp.Payload)
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.Status != zcl.StatusSuccess {
			return fmt.Errorf("write attribute 0x%04X status 0x%02X", r.AttributeID, r.Status)
		}
	}
	return nil
}

func (h *Hub) sendEwelink(ctx context.Context, nwk uint16, ep uint8, payload []byte) error {
	frame := &zcl.Frame{
		Control: zcl.FrameControl{
			FrameType:              zcl.FrameTypeCluster,
			ManufacturerSpecific:   true,
			Direction:              zcl.DirectionClientToServer,
			DisableDefaultResponse: false,
		},
		ManufCode: coolkitManuf,
		CommandID: ewelinkProtocolCmd,
		Payload:   payload,
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := h.relayExchange(c, nwk, ep, clusterEwelink, frame, uint8(zcl.CmdDefaultResponse))
	return err
}

// relayExchange sends one ZCL frame and waits on the AF listener.
// goznp's own wait stops at 5s and ignores the extended incoming command.
func (h *Hub) relayExchange(ctx context.Context, nwk uint16, ep uint8, cluster zcl.ClusterID, frame *zcl.Frame, want uint8) (*zcl.Frame, error) {
	z, ok := adapterZNP(h.adapter)
	if !ok {
		return nil, fmt.Errorf("no znp client")
	}
	h.radio.Lock()
	defer h.radio.Unlock()
	h.zclSeq++
	if h.zclSeq == 0 {
		h.zclSeq = 1
	}
	frame.TransSeqNum = h.zclSeq
	ch := make(chan *znp.IncomingMessage, 8)
	h.afMu.Lock()
	h.afWait = &afSlot{nwk: nwk, cluster: uint16(cluster), ch: ch}
	h.afMu.Unlock()
	defer func() {
		h.afMu.Lock()
		if h.afWait != nil && h.afWait.ch == ch {
			h.afWait = nil
		}
		h.afMu.Unlock()
	}()

	status, err := z.AfDataRequest(ctx, znp.DataRequest{
		DstAddr:     nwk,
		DstEndpoint: ep,
		SrcEndpoint: 1,
		ClusterID:   uint16(cluster),
		TransID:     frame.TransSeqNum,
		Options:     znp.AfOptionAckRequest,
		Radius:      30,
		Data:        frame.ToBytes(),
	})
	if err != nil {
		return nil, err
	}
	if status != 0 {
		return nil, fmt.Errorf("data request status 0x%02X", status)
	}
	confirm, err := z.WaitForDataConfirm(ctx, frame.TransSeqNum, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if confirm.Status != 0 {
		return nil, fmt.Errorf("data confirm 0x%02X", confirm.Status)
	}

	timer := time.NewTimer(relayWait)
	defer timer.Stop()
	var skipped []string
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("relay response timeout skipped=%s", strings.Join(skipped, ","))
		case msg := <-ch:
			parsed, err := zcl.ParseFrame(msg.Data)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("len:%d", len(msg.Data)))
				continue
			}
			if parsed.CommandID != want {
				skipped = append(skipped, fmt.Sprintf("cmd:0x%02X", parsed.CommandID))
				continue
			}
			return parsed, nil
		}
	}
}

func asU8(v any) uint8 {
	switch n := v.(type) {
	case uint8:
		return n
	case int8:
		return uint8(n)
	case bool:
		if n {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func asU16(v any) uint16 {
	switch n := v.(type) {
	case uint16:
		return n
	case uint8:
		return uint16(n)
	default:
		return 0
	}
}

func asI16(v any) int16 {
	switch n := v.(type) {
	case int16:
		return n
	case uint16:
		return int16(n)
	case uint8:
		return int16(n)
	default:
		return 0
	}
}

func asBool(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case uint8:
		return n != 0
	default:
		return false
	}
}

func (h *Hub) storeRelay(ieee string, r Relay) {
	now := time.Now()
	h.mu.Lock()
	if dev := h.devices[compactIEEE(ieee)]; dev != nil {
		cp := r
		dev.Relay = &cp
		dev.Reachable = true
		dev.Error = ""
		dev.LastSeen = &now
		dev.LastSeenMs = now.UnixMilli()
	}
	h.mu.Unlock()
	h.saveCache()
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
}

func relayEndpoint(d Device) uint8 {
	if d.Endpoint == 0 {
		return 1
	}
	return d.Endpoint
}

func powerOnValue(name string) (uint8, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "off":
		return 0, nil
	case "on":
		return 1, nil
	case "toggle":
		return 2, nil
	case "previous":
		return 255, nil
	default:
		return 0, fmt.Errorf("%w: power_on_behavior %q", errRelayInput, name)
	}
}

func powerOnName(v uint8) (string, bool) {
	switch v {
	case 0:
		return "off", true
	case 1:
		return "on", true
	case 2:
		return "toggle", true
	case 255:
		return "previous", true
	default:
		return "", false
	}
}

func triggerValue(name string) (uint8, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edge":
		return 0, nil
	case "pulse":
		return 1, nil
	case "following(off)":
		return 2, nil
	case "following(on)":
		return 130, nil
	default:
		return 0, fmt.Errorf("%w: external_trigger_mode %q", errRelayInput, name)
	}
}

func triggerName(v uint8) (string, bool) {
	switch v {
	case 0:
		return "edge", true
	case 1:
		return "pulse", true
	case 2:
		return "following(off)", true
	case 130:
		return "following(on)", true
	default:
		return "", false
	}
}

func turboValue(on bool) int16 {
	if on {
		return 0x14
	}
	return 0x09
}

func turboOn(v int16) (bool, bool) {
	switch v {
	case 0x14:
		return true, true
	case 0x09:
		return false, true
	default:
		return false, false
	}
}

func delayRaw(seconds float64) (uint16, error) {
	if seconds < 0.5 || seconds > 3599.5 {
		return 0, fmt.Errorf("%w: delayed_power_on_time out of range", errRelayInput)
	}
	scaled := seconds * 2
	raw := math.Round(scaled)
	if math.Abs(scaled-raw) > 0.05 || raw < 1 || raw > 7199 {
		return 0, fmt.Errorf("%w: delayed_power_on_time step is 0.5", errRelayInput)
	}
	return uint16(raw), nil
}

func inchingPayload(control, mode string, seconds float64) ([]byte, error) {
	ctrl := strings.ToUpper(strings.TrimSpace(control))
	md := strings.ToUpper(strings.TrimSpace(mode))
	if ctrl != "ENABLE" && ctrl != "DISABLE" {
		return nil, fmt.Errorf("%w: inching_control", errRelayInput)
	}
	if md != "ON" && md != "OFF" {
		return nil, fmt.Errorf("%w: inching_mode", errRelayInput)
	}
	raw, err := delayRaw(seconds)
	if err != nil {
		return nil, err
	}
	payload := []byte{
		0x01, 0x17, 0x07, 0x80, 0x00, 0x00,
		byte(raw), byte(raw >> 8), 0x00, 0x00, 0x00,
	}
	if ctrl == "ENABLE" {
		payload[4] |= 0x80
	}
	if md == "ON" {
		payload[4] |= 0x01
	}
	var sum byte
	for i := 0; i < 10; i++ {
		sum ^= payload[i]
	}
	payload[10] = sum
	return payload, nil
}
