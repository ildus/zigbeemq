package hub

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"time"
	"unsafe"

	"github.com/marstid/goznp/pkg/adapter"
	"github.com/marstid/goznp/pkg/unpi"
	"github.com/marstid/goznp/pkg/zcl"
	"github.com/marstid/goznp/pkg/znp"
)

const holdDelay = time.Second
const multiClickWait = 400 * time.Millisecond

const xiaomiClickAttr zcl.AttributeID = 0x8000

func adapterZNP(a *adapter.Adapter) (*znp.ZNP, bool) {
	if a == nil {
		return nil, false
	}
	v := reflect.ValueOf(a).Elem().FieldByName("znp")
	if !v.IsValid() || v.IsNil() {
		return nil, false
	}
	iface := reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Interface()
	z, ok := iface.(*znp.ZNP)
	return z, ok
}

func (h *Hub) hookIncoming() {
	z, ok := adapterZNP(h.adapter)
	if !ok {
		h.log.Warn("incoming: no znp client, clicks will not work")
		return
	}
	if h.incoming == nil {
		h.incoming = make(chan *znp.IncomingMessage, 128)
		go func() {
			for msg := range h.incoming {
				h.handleIncoming(msg)
			}
		}()
	}
	h.log.Info("incoming listener attached")
	z.OnFrame(func(frame *unpi.Frame) {
		if frame == nil || frame.Type != unpi.AREQ || frame.Subsystem != unpi.AF {
			return
		}
		var msg *znp.IncomingMessage
		var err error
		switch frame.CommandID {
		case znp.CmdAfIncomingMsg.ID:
			msg, err = parseAFIncoming(frame.Data)
		case znp.CmdAfIncomingMsgExt.ID:
			msg, err = parseAFIncomingExt(frame.Data)
		default:
			return
		}
		if err != nil {
			h.log.Info("incoming parse", "cmd", fmt.Sprintf("0x%02X", frame.CommandID), "err", err)
			return
		}
		select {
		case h.incoming <- msg:
		default:
			h.log.Warn("incoming queue full")
		}
	})
}

func (h *Hub) expandHAEndpoint(ctx context.Context) error {
	z, ok := adapterZNP(h.adapter)
	if !ok {
		return fmt.Errorf("no znp client")
	}
	cfg := znp.EndpointConfig{
		Endpoint:    1,
		AppProfID:   uint16(znp.ProfileHomeAutomation),
		AppDeviceID: 0x0005,
		AppDevVer:   1,
		InClusters: []uint16{
			uint16(zcl.ClusterBasic),
			uint16(zcl.ClusterIdentify),
			uint16(zcl.ClusterOnOff),
			uint16(zcl.ClusterLevelControl),
			uint16(zcl.ClusterMultistateInput),
			uint16(zcl.ClusterOccupancySensing),
			uint16(zcl.ClusterIASZone),
		},
		OutClusters: []uint16{
			uint16(zcl.ClusterBasic),
			uint16(zcl.ClusterOnOff),
			uint16(zcl.ClusterLevelControl),
			uint16(zcl.ClusterColorControl),
		},
	}
	// goznp Open already registered ep1 with Basic-only. Replace it.
	for try := 0; try < 4; try++ {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, _ = z.AfDelete(c, 1)
		status, err := z.AfRegister(c, cfg)
		cancel()
		if err != nil {
			return err
		}
		if status == 0 {
			h.log.Info("coordinator endpoint 1 accepts OnOff")
			return nil
		}
		h.log.Warn("af register ep1", "status", fmt.Sprintf("0x%02X", status), "try", try+1)
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("could not register OnOff on endpoint 1")
}

func parseAFIncoming(data []byte) (*znp.IncomingMessage, error) {
	if len(data) < 17 {
		return nil, fmt.Errorf("short")
	}
	off := 0
	u16 := func() uint16 {
		v := binary.LittleEndian.Uint16(data[off:])
		off += 2
		return v
	}
	u8 := func() uint8 {
		v := data[off]
		off++
		return v
	}
	group := u16()
	cluster := u16()
	src := u16()
	srcEp := u8()
	dstEp := u8()
	bcast := u8()
	lqi := u8()
	sec := u8()
	ts := binary.LittleEndian.Uint32(data[off:])
	off += 4
	trans := u8()
	n := int(u8())
	if off+n > len(data) {
		return nil, fmt.Errorf("payload")
	}
	payload := append([]byte(nil), data[off:off+n]...)
	return &znp.IncomingMessage{
		GroupID:      group,
		ClusterID:    cluster,
		SrcAddr:      src,
		SrcEndpoint:  srcEp,
		DstEndpoint:  dstEp,
		WasBroadcast: bcast != 0,
		LinkQuality:  lqi,
		SecurityUse:  sec != 0,
		Timestamp:    ts,
		TransSeqNum:  trans,
		Data:         payload,
	}, nil
}

func parseAFIncomingExt(data []byte) (*znp.IncomingMessage, error) {
	if len(data) < 27 {
		return nil, fmt.Errorf("short ext")
	}
	off := 0
	u16 := func() uint16 {
		v := binary.LittleEndian.Uint16(data[off:])
		off += 2
		return v
	}
	u8 := func() uint8 {
		v := data[off]
		off++
		return v
	}
	group := u16()
	cluster := u16()
	_ = u8() // addr mode
	src := binary.LittleEndian.Uint16(data[off:])
	off += 8
	srcEp := u8()
	_ = u16() // src PAN
	dstEp := u8()
	bcast := u8()
	lqi := u8()
	sec := u8()
	ts := binary.LittleEndian.Uint32(data[off:])
	off += 4
	trans := u8()
	n := int(u16())
	if off+n > len(data) {
		return nil, fmt.Errorf("payload")
	}
	payload := append([]byte(nil), data[off:off+n]...)
	return &znp.IncomingMessage{
		GroupID:      group,
		ClusterID:    cluster,
		SrcAddr:      src,
		SrcEndpoint:  srcEp,
		DstEndpoint:  dstEp,
		WasBroadcast: bcast != 0,
		LinkQuality:  lqi,
		SecurityUse:  sec != 0,
		Timestamp:    ts,
		TransSeqNum:  trans,
		Data:         payload,
	}, nil
}

func (h *Hub) handleIncoming(msg *znp.IncomingMessage) {
	if msg.ClusterID == uint16(zcl.ClusterOnOff) || msg.ClusterID == uint16(zcl.ClusterMultistateInput) ||
		msg.ClusterID == uint16(zcl.ClusterOccupancySensing) || msg.ClusterID == uint16(zcl.ClusterIASZone) {
		h.log.Info("incoming", "nwk", fmt.Sprintf("0x%04X", msg.SrcAddr), "cluster", fmt.Sprintf("0x%04X", msg.ClusterID), "len", len(msg.Data))
	}
	d, err := h.deviceForNwk(msg.SrcAddr)
	if err != nil {
		if msg.ClusterID == uint16(zcl.ClusterOnOff) {
			h.log.Info("incoming unmatched switch-like frame", "nwk", fmt.Sprintf("0x%04X", msg.SrcAddr))
		}
		return
	}
	frame, err := zcl.ParseFrame(msg.Data)
	if err != nil {
		if d.Kind == KindSwitch {
			h.log.Info("incoming zcl", "nwk", fmt.Sprintf("0x%04X", msg.SrcAddr), "err", err)
		}
		return
	}

	if d.Kind == KindLight || d.Kind == KindPlug {
		h.applyLightReport(d.IEEE, frame, msg.ClusterID)
		return
	}

	occCluster := msg.ClusterID == uint16(zcl.ClusterOccupancySensing) || msg.ClusterID == uint16(zcl.ClusterIASZone)
	if d.Kind == KindMotion || d.HasOccupancy || (occCluster && (d.Kind == KindSensor || d.Kind == KindUnknown)) {
		h.maybeSetupMotion(d)
		if msg.ClusterID == uint16(zcl.ClusterIASZone) && frame.Control.FrameType == zcl.FrameTypeCluster && frame.CommandID == zcl.CmdIASZoneEnrollRequest {
			go h.enrollIASZone(d.IEEE, msg.SrcEndpoint)
			return
		}
		if h.applyMotionReport(d.IEEE, frame, msg.ClusterID) {
			return
		}
	}

	if msg.ClusterID == uint16(zcl.ClusterOnOff) && frame.Control.FrameType == zcl.FrameTypeGlobal && frame.CommandID == uint8(zcl.CmdReportAttributes) {
		if count, ok := xiaomiClickCount(frame); ok {
			// Values 2+ are final multi-click reports. Values 0/1 are
			// press/single metadata; OnOff press/release handles those.
			if count >= 2 {
				h.cancelPress(d.IEEE)
				h.noteAction(d.IEEE, clickAction(count))
			}
			return
		}
	}

	if action := decodeAction(d, frame, msg.ClusterID); action != "" {
		h.cancelPress(d.IEEE)
		h.noteAction(d.IEEE, action)
		return
	}

	if msg.ClusterID == uint16(zcl.ClusterOnOff) && frame.Control.FrameType == zcl.FrameTypeGlobal && frame.CommandID == uint8(zcl.CmdReportAttributes) {
		h.handleXiaomiOnOff(d.IEEE, frame)
		return
	}
}

func (h *Hub) deviceForNwk(nwk uint16) (Device, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var fallback *Device
	switches := 0
	for _, d := range h.devices {
		if d.NwkAddr == nwk {
			return *d, nil
		}
		if d.Kind == KindSwitch {
			switches++
			fallback = d
		}
	}
	if switches == 1 && fallback != nil {
		fallback.NwkAddr = nwk
		return *fallback, nil
	}
	return Device{}, errNotFound
}

func decodeAction(d Device, frame *zcl.Frame, cluster uint16) string {
	if cluster == uint16(zcl.ClusterOnOff) && frame.Control.FrameType == zcl.FrameTypeCluster {
		switch frame.CommandID {
		case zcl.CmdOnOffOff:
			return "single"
		case zcl.CmdOnOffOn:
			return "on"
		case zcl.CmdOnOffToggle:
			return "toggle"
		}
	}
	if cluster == uint16(zcl.ClusterMultistateInput) && frame.CommandID == uint8(zcl.CmdReportAttributes) {
		attrs, err := zcl.ParseReportAttributesPayload(frame.Payload)
		if err != nil {
			return ""
		}
		for _, a := range attrs {
			if a.AttributeID != zcl.AttrMultistateInputPresentValue {
				continue
			}
			n := toUint(a.Value)
			switch n {
			case 0:
				return "hold"
			case 1:
				return "single"
			case 2:
				return "double"
			case 3:
				return "triple"
			case 255:
				return "release"
			default:
				if n >= 4 {
					return "many"
				}
			}
		}
	}
	_ = d
	return ""
}

func xiaomiClickCount(frame *zcl.Frame) (uint8, bool) {
	attrs, err := zcl.ParseReportAttributesPayload(frame.Payload)
	if err != nil {
		return 0, false
	}
	for _, a := range attrs {
		if a.AttributeID == xiaomiClickAttr {
			return uint8(toUint(a.Value)), true
		}
	}
	return 0, false
}

func clickAction(count uint8) string {
	switch count {
	case 1:
		return "single"
	case 2:
		return "double"
	case 3:
		return "triple"
	case 4:
		return "quadruple"
	default:
		return "many"
	}
}

func (h *Hub) handleXiaomiOnOff(ieee string, frame *zcl.Frame) {
	attrs, err := zcl.ParseReportAttributesPayload(frame.Payload)
	if err != nil {
		return
	}
	var onOff *bool
	for _, a := range attrs {
		if a.AttributeID != zcl.AttrOnOff {
			continue
		}
		switch v := a.Value.(type) {
		case bool:
			onOff = &v
		default:
			b := toUint(v) != 0
			onOff = &b
		}
	}
	if onOff == nil {
		return
	}
	if !*onOff {
		h.startPress(ieee)
		return
	}
	h.finishPress(ieee)
}

func (h *Hub) startPress(ieee string) {
	h.pressMu.Lock()
	w := h.presses[ieee]
	flushSingle := w != nil && w.up && !w.held
	if w != nil && w.timer != nil {
		w.timer.Stop()
	}
	n := &pressWatch{}
	n.timer = time.AfterFunc(holdDelay, func() {
		h.pressMu.Lock()
		cur := h.presses[ieee]
		if cur != n {
			h.pressMu.Unlock()
			return
		}
		cur.held = true
		h.pressMu.Unlock()
		h.noteAction(ieee, "hold")
	})
	h.presses[ieee] = n
	h.pressMu.Unlock()
	if flushSingle {
		h.noteAction(ieee, "single")
	}
}

func (h *Hub) finishPress(ieee string) {
	h.pressMu.Lock()
	w := h.presses[ieee]
	if w != nil && w.timer != nil {
		w.timer.Stop()
	}
	if w == nil {
		h.pressMu.Unlock()
		return
	}
	if w.held {
		delete(h.presses, ieee)
		h.pressMu.Unlock()
		h.noteAction(ieee, "release")
		return
	}
	n := &pressWatch{up: true}
	n.timer = time.AfterFunc(multiClickWait, func() {
		h.pressMu.Lock()
		cur := h.presses[ieee]
		if cur != n {
			h.pressMu.Unlock()
			return
		}
		delete(h.presses, ieee)
		h.pressMu.Unlock()
		h.noteAction(ieee, "single")
	})
	h.presses[ieee] = n
	h.pressMu.Unlock()
}

func (h *Hub) cancelPress(ieee string) {
	h.pressMu.Lock()
	w := h.presses[ieee]
	delete(h.presses, ieee)
	h.pressMu.Unlock()
	if w != nil && w.timer != nil {
		w.timer.Stop()
	}
}

func (h *Hub) noteAction(ieee, action string) {
	now := time.Now()
	h.mu.Lock()
	dev := h.devices[compactIEEE(ieee)]
	if dev == nil {
		h.mu.Unlock()
		return
	}
	dev.LastClick = action
	dev.ClickSeq++
	dev.Reachable = true
	dev.Error = ""
	dev.LastSeen = &now
	dev.LastSeenMs = now.UnixMilli()
	seq := dev.ClickSeq
	h.mu.Unlock()
	h.log.Info("click", "name", mustDevice(h, ieee).Name, "action", action, "seq", seq)
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
}

func (h *Hub) applyMotionReport(ieee string, frame *zcl.Frame, cluster uint16) bool {
	if cluster == uint16(zcl.ClusterOccupancySensing) && frame.CommandID == uint8(zcl.CmdReportAttributes) {
		attrs, err := zcl.ParseReportAttributesPayload(frame.Payload)
		if err != nil {
			return false
		}
		for _, a := range attrs {
			if a.AttributeID != zcl.AttrOccupancyValue {
				continue
			}
			if occ, ok := occupancyFromValue(a.Value); ok {
				h.noteOccupancy(ieee, occ)
				return true
			}
		}
	}
	if cluster == uint16(zcl.ClusterIASZone) && frame.Control.FrameType == zcl.FrameTypeCluster && frame.CommandID == zcl.CmdIASZoneStatusChangeNotification {
		if len(frame.Payload) < 2 {
			return false
		}
		status := uint16(frame.Payload[0]) | uint16(frame.Payload[1])<<8
		occ := status&zcl.IASZoneStatusAlarm1 != 0 || status&zcl.IASZoneStatusAlarm2 != 0
		h.noteOccupancy(ieee, occ)
		return true
	}
	return false
}

func (h *Hub) noteOccupancy(ieee string, occ bool) {
	now := time.Now()
	h.mu.Lock()
	dev := h.devices[compactIEEE(ieee)]
	if dev == nil {
		h.mu.Unlock()
		return
	}
	changed := dev.Occupancy == nil || *dev.Occupancy != occ
	meta := false
	dev.Occupancy = &occ
	if !dev.HasOccupancy {
		dev.HasOccupancy = true
		meta = true
	}
	if dev.Kind == KindSensor || dev.Kind == KindUnknown {
		dev.Kind = KindMotion
		meta = true
	}
	dev.Reachable = true
	dev.Error = ""
	dev.LastSeen = &now
	dev.LastSeenMs = now.UnixMilli()
	h.mu.Unlock()
	if meta {
		h.saveCache()
	}
	if !changed && !meta {
		return
	}
	h.log.Info("occupancy", "name", mustDevice(h, ieee).Name, "value", occ)
	h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
}

func (h *Hub) maybeSetupMotion(d Device) {
	key := compactIEEE(d.IEEE)
	h.boundMu.Lock()
	if _, ok := h.motionReady[key]; ok {
		h.boundMu.Unlock()
		return
	}
	h.motionReady[key] = struct{}{}
	h.boundMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		addr, err := parseIEEE(d.IEEE)
		if err != nil {
			h.clearMotionReady(key)
			return
		}
		bound := false
		if ep := endpointWith(d, "IASZone"); ep != 0 {
			h.radio.Lock()
			_ = h.adapter.WriteIASZoneCIEAddress(ctx, d.NwkAddr, ep, h.coordIEEE)
			err = h.adapter.Bind(ctx, addr, d.NwkAddr, ep, uint16(zcl.ClusterIASZone), h.coordIEEE, 1)
			h.radio.Unlock()
			if err != nil {
				h.log.Info("motion bind skipped", "name", d.Name, "cluster", "IASZone", "err", err)
			} else {
				h.log.Info("motion bound", "name", d.Name, "cluster", "IASZone")
				bound = true
			}
		}
		if ep := endpointWith(d, "OccupancySensing"); ep != 0 {
			h.radio.Lock()
			err = h.adapter.Bind(ctx, addr, d.NwkAddr, ep, uint16(zcl.ClusterOccupancySensing), h.coordIEEE, 1)
			if err == nil {
				_ = h.adapter.ConfigureReporting(ctx, d.NwkAddr, ep, zcl.ClusterOccupancySensing, zcl.AttrOccupancyValue, zcl.TypeBitmap8, 0, 3600, uint8(1))
			}
			h.radio.Unlock()
			if err != nil {
				h.log.Info("motion bind skipped", "name", d.Name, "cluster", "OccupancySensing", "err", err)
			} else {
				h.log.Info("motion bound", "name", d.Name, "cluster", "OccupancySensing")
				bound = true
			}
		}
		if !bound {
			h.clearMotionReady(key)
		}
	}()
}

func (h *Hub) enrollIASZone(ieee string, ep uint8) {
	d, err := h.Device(ieee)
	if err != nil {
		return
	}
	if ep == 0 {
		ep = d.Endpoint
	}
	if ep == 0 {
		ep = 1
	}
	zoneID := zoneIDForIEEE(ieee)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	h.radio.Lock()
	err = h.adapter.EnrollIASZone(ctx, d.NwkAddr, ep, 0, zoneID)
	h.radio.Unlock()
	if err != nil {
		h.log.Info("ias enroll failed", "name", d.Name, "err", err)
		return
	}
	h.log.Info("ias enrolled", "name", d.Name, "zone", zoneID)
}

func (h *Hub) clearMotionReady(key string) {
	h.boundMu.Lock()
	delete(h.motionReady, key)
	h.boundMu.Unlock()
}

func zoneIDForIEEE(ieee string) uint8 {
	var n uint8 = 1
	for i := 0; i < len(ieee); i++ {
		n += ieee[i]
	}
	if n == 0 {
		n = 1
	}
	return n
}

func (h *Hub) applyLightReport(ieee string, frame *zcl.Frame, cluster uint16) {
	if cluster != uint16(zcl.ClusterOnOff) || frame.CommandID != uint8(zcl.CmdReportAttributes) {
		return
	}
	attrs, err := zcl.ParseReportAttributesPayload(frame.Payload)
	if err != nil {
		return
	}
	now := time.Now()
	h.mu.Lock()
	dev := h.devices[compactIEEE(ieee)]
	if dev == nil {
		h.mu.Unlock()
		return
	}
	changed := false
	for _, a := range attrs {
		if a.AttributeID != zcl.AttrOnOff {
			continue
		}
		on := false
		switch v := a.Value.(type) {
		case bool:
			on = v
		default:
			on = toUint(v) != 0
		}
		dev.On = &on
		dev.Reachable = true
		dev.LastSeen = &now
		changed = true
	}
	h.mu.Unlock()
	if changed {
		h.broadcast(Event{Type: "device", Data: mustDevice(h, ieee)})
	}
}

func (h *Hub) maybeBindSwitch(d Device) {
	key := compactIEEE(d.IEEE)
	h.boundMu.Lock()
	if _, ok := h.bound[key]; ok {
		h.boundMu.Unlock()
		return
	}
	h.bound[key] = struct{}{}
	h.boundMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		addr, err := parseIEEE(d.IEEE)
		if err != nil {
			h.clearBound(key)
			return
		}
		ep := d.Endpoint
		if ep == 0 {
			ep = 1
		}
		h.radio.Lock()
		err = h.adapter.Bind(ctx, addr, d.NwkAddr, ep, uint16(zcl.ClusterOnOff), h.coordIEEE, 1)
		h.radio.Unlock()
		if err != nil {
			h.log.Info("switch bind skipped", "name", d.Name, "err", err)
			h.clearBound(key)
			return
		}
		h.log.Info("switch bound", "name", d.Name)
	}()
}

func (h *Hub) clearBound(key string) {
	h.boundMu.Lock()
	delete(h.bound, key)
	h.boundMu.Unlock()
}

func toUint(v any) uint64 {
	switch x := v.(type) {
	case uint8:
		return uint64(x)
	case uint16:
		return uint64(x)
	case uint32:
		return uint64(x)
	case uint64:
		return x
	case int:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	default:
		return 0
	}
}
