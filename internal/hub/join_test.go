package hub

import (
	"testing"

	"github.com/marstid/goznp/pkg/adapter"
)

func TestEventIEEEPrefersDevice(t *testing.T) {
	var addr [8]byte
	addr[0] = 0x42
	ev := adapter.DeviceEvent{
		Type:   adapter.DeviceEventJoined,
		Device: &adapter.Device{IEEEAddr: addr, NwkAddr: 0x3F72},
	}
	if got := eventIEEE(ev); got != "0000000000000042" {
		t.Fatalf("ieee %s", got)
	}
	left := adapter.DeviceEvent{Type: adapter.DeviceEventLeft, IEEEAddr: addr}
	if got := eventIEEE(left); got != "0000000000000042" {
		t.Fatalf("leave ieee %s", got)
	}
	if eventIEEE(adapter.DeviceEvent{Type: adapter.DeviceEventJoined}) != "" {
		t.Fatal("zero ieee leaked")
	}
}

func TestNoteJoinedKeepsUnknown(t *testing.T) {
	h := testHub(t)
	h.forgotten["0011223344556677"] = struct{}{}
	if !h.noteJoined("00:11:22:33:44:55:66:77", 0x1234) {
		t.Fatal("rejected join")
	}
	d, err := h.Device("0011223344556677")
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != KindUnknown || d.Name != "0011223344556677" || d.NwkAddr != 0x1234 || !d.New || !d.Reachable || d.Archived {
		t.Fatalf("%+v", d)
	}
	if _, ok := h.forgotten["0011223344556677"]; ok {
		t.Fatal("still forgotten")
	}
	if h.noteJoined("0000000000000000", 1) {
		t.Fatal("accepted zero ieee")
	}
	if _, err := h.Device("0000000000000000"); err == nil {
		t.Fatal("phantom device")
	}
}

func TestNoteJoinedPreservesLabel(t *testing.T) {
	h := testHub(t, Device{
		IEEE: "0011223344556677", Name: "Relay", Kind: KindPlug, UserLabel: true, Archived: true, NwkAddr: 9,
	})
	if !h.noteJoined("0011223344556677", 0) {
		t.Fatal("rejected")
	}
	d, err := h.Device("0011223344556677")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Relay" || d.Kind != KindPlug || !d.Archived || !d.UserLabel || d.NwkAddr != 9 || !d.Reachable {
		t.Fatalf("%+v", d)
	}
}
