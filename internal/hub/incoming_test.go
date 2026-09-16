package hub

import (
	"encoding/binary"
	"github.com/marstid/goznp/pkg/zcl"
	"io"
	"log/slog"
	"testing"
)

func testHub(t *testing.T, ds ...Device) *Hub {
	t.Helper()
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	for i := range ds {
		d := ds[i]
		h.devices[compactIEEE(d.IEEE)] = &d
	}
	return h
}
func TestParseAFIncoming(t *testing.T) {
	d := make([]byte, 20)
	binary.LittleEndian.PutUint16(d, 0x1234)
	binary.LittleEndian.PutUint16(d[2:], 6)
	binary.LittleEndian.PutUint16(d[4:], 0xabcd)
	d[6], d[7], d[8], d[9], d[10] = 2, 1, 1, 99, 1
	binary.LittleEndian.PutUint32(d[11:], 0x10203040)
	d[15], d[16] = 7, 3
	copy(d[17:], []byte{1, 2, 3})
	got, e := parseAFIncoming(d)
	if e != nil {
		t.Fatal(e)
	}
	if got.GroupID != 0x1234 || got.ClusterID != 6 || got.SrcAddr != 0xabcd || !got.WasBroadcast || !got.SecurityUse || got.LinkQuality != 99 || got.Timestamp != 0x10203040 || got.TransSeqNum != 7 {
		t.Fatalf("bad message: %+v", got)
	}
	d[17] = 9
	if got.Data[0] != 1 {
		t.Fatal("payload aliases input")
	}
}
func TestParseAFIncomingMalformed(t *testing.T) {
	if _, e := parseAFIncoming(make([]byte, 16)); e == nil {
		t.Fatal("accepted short")
	}
	d := make([]byte, 17)
	d[16] = 1
	if _, e := parseAFIncoming(d); e == nil {
		t.Fatal("accepted truncated payload")
	}
}
func TestParseAFIncomingExt(t *testing.T) {
	d := make([]byte, 29)
	binary.LittleEndian.PutUint16(d, 0x102)
	binary.LittleEndian.PutUint16(d[2:], 0x500)
	d[4] = 3
	binary.LittleEndian.PutUint16(d[5:], 0x3344)
	d[13] = 4
	binary.LittleEndian.PutUint16(d[14:], 0x7788)
	d[16], d[17], d[18], d[19] = 1, 1, 88, 1
	binary.LittleEndian.PutUint32(d[20:], 0x55667788)
	d[24] = 9
	binary.LittleEndian.PutUint16(d[25:], 2)
	d[27], d[28] = 0xaa, 0xbb
	got, e := parseAFIncomingExt(d)
	if e != nil {
		t.Fatal(e)
	}
	if got.SrcAddr != 0x3344 || got.SrcEndpoint != 4 || got.DstEndpoint != 1 || len(got.Data) != 2 {
		t.Fatalf("bad ext: %+v", got)
	}
}
func TestParseAFIncomingExtMalformed(t *testing.T) {
	for n := 0; n <= 27; n++ {
		d := make([]byte, n)
		if n >= 27 {
			binary.LittleEndian.PutUint16(d[25:], 1)
		}
		if _, e := parseAFIncomingExt(d); e == nil {
			t.Fatalf("accepted len %d", n)
		}
	}
}
func report(attr zcl.AttributeID, typ zcl.DataType, v byte) *zcl.Frame {
	return &zcl.Frame{Control: zcl.FrameControl{FrameType: zcl.FrameTypeGlobal}, CommandID: uint8(zcl.CmdReportAttributes), Payload: []byte{byte(attr), byte(attr >> 8), byte(typ), v}}
}
func TestDecodeAction(t *testing.T) {
	cf := func(c uint8) *zcl.Frame {
		return &zcl.Frame{Control: zcl.FrameControl{FrameType: zcl.FrameTypeCluster}, CommandID: c}
	}
	cases := []struct {
		f *zcl.Frame
		c uint16
		w string
	}{{cf(zcl.CmdOnOffOff), uint16(zcl.ClusterOnOff), "single"}, {cf(zcl.CmdOnOffToggle), uint16(zcl.ClusterOnOff), "toggle"}, {report(zcl.AttrMultistateInputPresentValue, zcl.TypeUint8, 2), uint16(zcl.ClusterMultistateInput), "double"}}
	for _, x := range cases {
		if g := decodeAction(Device{}, x.f, x.c); g != x.w {
			t.Errorf("got %q want %q", g, x.w)
		}
	}
}
func TestXiaomiClickCount(t *testing.T) {
	for _, raw := range []byte{0, 1, 2, 3, 4, 255} {
		f := report(xiaomiClickAttr, zcl.TypeUint8, raw)
		got, ok := xiaomiClickCount(f)
		if !ok || got != raw {
			t.Fatalf("count = %d,%v; want %d,true", got, ok, raw)
		}
	}
}

func TestClickActionUsesReportedGestureCount(t *testing.T) {
	cases := map[uint8]string{2: "double", 3: "triple", 4: "quadruple", 5: "many"}
	for count, want := range cases {
		if got := clickAction(count); got != want {
			t.Fatalf("clickAction(%d) = %q, want %q", count, got, want)
		}
	}
}

func TestReportsUpdateState(t *testing.T) {
	d := Device{IEEE: "00124b0000000001", Name: "sensor", Kind: KindSensor}
	h := testHub(t, d)
	if !h.applyMotionReport(d.IEEE, report(zcl.AttrOccupancyValue, zcl.TypeBitmap8, 1), uint16(zcl.ClusterOccupancySensing)) {
		t.Fatal("not handled")
	}
	g, _ := h.Device(d.IEEE)
	if g.Occupancy == nil || !*g.Occupancy || g.Kind != KindMotion || !g.HasOccupancy {
		t.Fatalf("bad motion: %+v", g)
	}
	d = Device{IEEE: "00124b0000000002", Name: "light", Kind: KindLight}
	h = testHub(t, d)
	h.applyLightReport(d.IEEE, report(zcl.AttrOnOff, zcl.TypeBoolean, 1), uint16(zcl.ClusterOnOff))
	g, _ = h.Device(d.IEEE)
	if g.On == nil || !*g.On || !g.Reachable || g.LastSeen == nil {
		t.Fatalf("bad light: %+v", g)
	}
}
func TestValueHelpers(t *testing.T) {
	if toUint(int(-1)) != 0 || toUint(true) != 1 || toUint("7") != 0 {
		t.Fatal("toUint")
	}
	if v, ok := occupancyFromValue(uint8(3)); !ok || !v {
		t.Fatal("occupancy")
	}
	if _, ok := occupancyFromValue("1"); ok {
		t.Fatal("unsupported occupancy")
	}
}
