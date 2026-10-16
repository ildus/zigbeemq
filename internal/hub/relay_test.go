package hub

import (
	"context"
	"testing"

	"github.com/marstid/goznp/pkg/zcl"
	"github.com/marstid/goznp/pkg/znp"
)

func TestRelayCodecs(t *testing.T) {
	v, err := powerOnValue(" Previous ")
	if err != nil || v != 255 {
		t.Fatalf("power %d %v", v, err)
	}
	if name, ok := powerOnName(2); !ok || name != "toggle" {
		t.Fatalf("name %s %v", name, ok)
	}
	if _, err := powerOnValue("last"); err == nil || !IsRelayInput(err) {
		t.Fatal(err)
	}
	tr, err := triggerValue("following(on)")
	if err != nil || tr != 130 {
		t.Fatalf("trigger %d %v", tr, err)
	}
	if name, ok := triggerName(2); !ok || name != "following(off)" {
		t.Fatalf("trigger name %s", name)
	}
	if turboValue(true) != 0x14 || turboValue(false) != 0x09 {
		t.Fatal("turbo")
	}
	if on, ok := turboOn(0x09); !ok || on {
		t.Fatal("turbo decode")
	}
	raw, err := delayRaw(1.5)
	if err != nil || raw != 3 {
		t.Fatalf("delay %d %v", raw, err)
	}
	if _, err := delayRaw(0.6); err == nil {
		t.Fatal("accepted bad step")
	}
	payload, err := inchingPayload("enable", "off", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 11 || payload[4] != 0x80 || payload[6] != 2 {
		t.Fatalf("payload %x", payload)
	}
	var sum byte
	for i := 0; i < 10; i++ {
		sum ^= payload[i]
	}
	if payload[10] != sum {
		t.Fatalf("checksum %x want %x", payload[10], sum)
	}
	if !IsSonoffMini(Device{Model: "ZBMINIR2"}) || IsSonoffMini(Device{Model: "RB 267"}) {
		t.Fatal("model")
	}
}

func TestApplyRelayReadBothClusters(t *testing.T) {
	var r Relay
	startup := applyRelayRead(&r, []zcl.ReadAttributeResult{{
		AttributeID: zcl.AttrStartUpOnOff,
		Status:      zcl.StatusSuccess,
		Value:       uint8(0),
	}})
	ewelink := applyRelayRead(&r, []zcl.ReadAttributeResult{
		{AttributeID: attrNetworkLed, Status: zcl.StatusSuccess, Value: true},
		{AttributeID: attrRadioPower, Status: zcl.StatusSuccess, Value: int16(0x09)},
		{AttributeID: attrDelayedState, Status: zcl.StatusSuccess, Value: false},
		{AttributeID: attrDelayedTime, Status: zcl.StatusSuccess, Value: uint16(0x41)},
		{AttributeID: attrExternalTrigger, Status: zcl.StatusSuccess, Value: uint8(0)},
		{AttributeID: attrDetachRelay, Status: zcl.StatusSuccess, Value: false},
	})
	if !startup || !ewelink || r.PowerOnBehavior != "off" || r.NetworkIndicator == nil || !*r.NetworkIndicator {
		t.Fatalf("indicator %+v", r)
	}
	if r.TurboMode == nil || *r.TurboMode || r.DelayedPowerOnTime == nil || *r.DelayedPowerOnTime != 32.5 || r.ExternalTriggerMode != "edge" || r.DetachRelayMode == nil || *r.DetachRelayMode {
		t.Fatalf("%+v", r)
	}
}

func TestInferKindSeparatesRelayAndPlug(t *testing.T) {
	onOff := []uint16{uint16(zcl.ClusterOnOff)}
	if inferKind("SONOFF", "ZBMINIR2", onOff) != KindRelay || inferKind("SONOFF", "MINI-ZBD", onOff) != KindRelay {
		t.Fatal("mini")
	}
	if inferKind("", "S26R2ZB", onOff) != KindPlug {
		t.Fatal("plug")
	}
}

func TestApplyRelayRead(t *testing.T) {
	var r Relay
	on := true
	if !applyRelayRead(&r, []zcl.ReadAttributeResult{{
		AttributeID: zcl.AttrStartUpOnOff,
		Status:      zcl.StatusSuccess,
		Value:       uint8(255),
	}, {
		AttributeID: attrNetworkLed,
		Status:      zcl.StatusSuccess,
		Value:       on,
	}}) || r.PowerOnBehavior != "previous" || r.NetworkIndicator == nil || !*r.NetworkIndicator {
		t.Fatalf("%+v", r)
	}
	if applyRelayRead(&r, []zcl.ReadAttributeResult{{
		AttributeID: zcl.AttrStartUpOnOff,
		Status:      0x86,
	}}) {
		t.Fatal("unsupported counted as read")
	}
}

func TestNoteAFFiltersCluster(t *testing.T) {
	h := testHub(t)
	ch := make(chan *znp.IncomingMessage, 1)
	h.afWait = &afSlot{nwk: 0x3F72, cluster: uint16(clusterEwelink), ch: ch}
	h.noteAF(&znp.IncomingMessage{SrcAddr: 0x3F72, ClusterID: uint16(zcl.ClusterOnOff)})
	h.noteAF(&znp.IncomingMessage{SrcAddr: 0x3F72, ClusterID: uint16(clusterEwelink)})
	if len(ch) != 1 {
		t.Fatalf("queued %d", len(ch))
	}
}

func TestApplyRelayRejectsInput(t *testing.T) {
	h := testHub(t, Device{IEEE: "0011223344556677", Model: "ZBMINIR2"})
	bad := "nope"
	if _, err := h.ApplyRelay(context.Background(), "0011223344556677", RelayPatch{PowerOnBehavior: &bad}); !IsRelayInput(err) {
		t.Fatal(err)
	}
	if _, err := h.ApplyRelay(context.Background(), "0011223344556677", RelayPatch{}); !IsRelayInput(err) {
		t.Fatal(err)
	}
	on := "previous"
	if _, err := h.ApplyRelay(context.Background(), "0011223344556677", RelayPatch{PowerOnBehavior: &on}); err == nil || IsRelayInput(err) || IsNotRelay(err) {
		t.Fatal(err)
	}
	other := testHub(t, Device{IEEE: "0011223344556677", Model: "RB 267"})
	if _, err := other.ApplyRelay(context.Background(), "0011223344556677", RelayPatch{PowerOnBehavior: &on}); !IsNotRelay(err) {
		t.Fatal(err)
	}
}
