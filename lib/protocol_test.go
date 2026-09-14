package probetool

import (
	"bytes"
	"testing"
	"time"
)

// ==========================================
// CRC8
// ==========================================

func TestCalcCRC8(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want uint8
	}{
		{"empty", nil, 0x00},
		{"single 0x01", []byte{0x01}, 0x01},
		{"wrap over 255", []byte{0xFF, 0x02}, 0x01}, // 0xFF+0x02 = 0x101 & 0xFF = 0x01
		{"known vector", []byte{0x01, 0x02, 0x03}, 0x06},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CalcCRC8(c.data); got != c.want {
				t.Fatalf("CalcCRC8(%v) = 0x%02x, want 0x%02x", c.data, got, c.want)
			}
		})
	}
}

// ==========================================
// Encode / Decode round-trip
// ==========================================

func TestEncodeDecodeEmpty(t *testing.T) {
	packets := EncodeRequest(5, 0, nil)
	if len(packets) != 1 {
		t.Fatalf("empty payload should produce 1 packet, got %d", len(packets))
	}
	if len(packets[0]) != RequestTSize {
		t.Fatalf("empty payload packet len = %d, want %d", len(packets[0]), RequestTSize)
	}

	info, total, dsize, doff, data, err := DecodeRequest(packets[0])
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if info.Fn != 5 || info.Stage != 0 {
		t.Fatalf("fn/stage = %d/%d, want 5/0", info.Fn, info.Stage)
	}
	if total != 0 || dsize != 0 || doff != 0 {
		t.Fatalf("sizes = %d/%d/%d, want 0/0/0", total, dsize, doff)
	}
	if len(data) != 0 {
		t.Fatalf("data len = %d, want 0", len(data))
	}
}

func TestEncodeDecodeSingleFragment(t *testing.T) {
	payload := []byte("13 20260914120000")
	packets := EncodeRequest(13, 0, payload)
	if len(packets) != 1 {
		t.Fatalf("small payload should be 1 packet, got %d", len(packets))
	}

	info, total, dsize, doff, data, err := DecodeRequest(packets[0])
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if info.Fn != 13 {
		t.Fatalf("fn = %d, want 13", info.Fn)
	}
	if total != uint32(len(payload)) || dsize != uint32(len(payload)) || doff != 0 {
		t.Fatalf("sizes = %d/%d/%d, want %d/%d/0", total, dsize, doff, len(payload), len(payload))
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("data = %q, want %q", data, payload)
	}
}

func TestEncodeFragmentationBoundaries(t *testing.T) {
	cases := []struct {
		size       int
		wantPacket int
	}{
		{0, 1},
		{1, 1},
		{MaxFragmentPayloadSize - 1, 1},
		{MaxFragmentPayloadSize, 1},
		{MaxFragmentPayloadSize + 1, 2},
		{MaxFragmentPayloadSize * 2, 2},
		{MaxFragmentPayloadSize*2 + 1, 3},
	}
	for _, c := range cases {
		data := bytes.Repeat([]byte{0xAB}, c.size)
		packets := EncodeRequest(1, 0, data)
		if len(packets) != c.wantPacket {
			t.Errorf("size %d: got %d packets, want %d", c.size, len(packets), c.wantPacket)
		}
	}
}

// Every fragment of a multi-fragment message must fit the client's 128-byte buffer.
func TestFragmentsFitClientBuffer(t *testing.T) {
	const clientBuf = 128
	data := bytes.Repeat([]byte{0x5A}, MaxFragmentPayloadSize*5+17)
	for i, p := range EncodeRequest(1, 0, data) {
		if len(p) > clientBuf {
			t.Fatalf("fragment %d len = %d exceeds client buffer %d", i, len(p), clientBuf)
		}
	}
}

// ==========================================
// Decode validation
// ==========================================

func TestDecodeRejectsBadMagic(t *testing.T) {
	packet := EncodeRequest(1, 0, []byte("hi"))[0]
	packet[0] ^= 0xFF // corrupt magic
	if _, _, _, _, _, err := DecodeRequest(packet); err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}

func TestDecodeRejectsBadCRC(t *testing.T) {
	packet := EncodeRequest(1, 0, []byte("hi"))[0]
	packet[RequestTSize] ^= 0xFF // corrupt payload byte
	if _, _, _, _, _, err := DecodeRequest(packet); err == nil {
		t.Fatal("expected error for bad CRC, got nil")
	}
}

func TestDecodeRejectsShortBuffer(t *testing.T) {
	if _, _, _, _, _, err := DecodeRequest(make([]byte, RequestTSize-1)); err == nil {
		t.Fatal("expected error for short buffer, got nil")
	}
}

func TestDecodeRejectsOversizedDataSize(t *testing.T) {
	packet := EncodeRequest(1, 0, []byte("hi"))[0]
	// dataSize lives at bytes [8:12], little-endian.
	packet[8] = 0xFF
	packet[9] = 0xFF
	if _, _, _, _, _, err := DecodeRequest(packet); err == nil {
		t.Fatal("expected error for oversized dataSize, got nil")
	}
}

// ==========================================
// Reassembler
// ==========================================

func TestReassemblerSingleFragment(t *testing.T) {
	r := NewReassembler(100 * time.Millisecond)
	defer r.StopCleanup()

	full, err := r.AddFragment("1.2.3.4", 1, 0, 5, 5, 0, []byte("hello"))
	if err != nil {
		t.Fatalf("AddFragment error: %v", err)
	}
	if string(full) != "hello" {
		t.Fatalf("got %q, want %q", full, "hello")
	}
}

func TestReassemblerMultiFragmentInOrder(t *testing.T) {
	r := NewReassembler(100 * time.Millisecond)
	defer r.StopCleanup()

	data := bytes.Repeat([]byte("x"), MaxFragmentPayloadSize*2+10)
	packets := EncodeRequest(7, 0, data)

	var full []byte
	for _, p := range packets {
		info, total, dsize, doff, frag, err := DecodeRequest(p)
		if err != nil {
			t.Fatalf("DecodeRequest: %v", err)
		}
		out, err := r.AddFragment("1.2.3.4", info.Fn, info.Stage, total, dsize, doff, frag)
		if err != nil {
			t.Fatalf("AddFragment: %v", err)
		}
		if out != nil {
			full = out
		}
	}
	if !bytes.Equal(full, data) {
		t.Fatalf("reassembled %d bytes, want %d", len(full), len(data))
	}
}

func TestReassemblerMultiFragmentOutOfOrder(t *testing.T) {
	r := NewReassembler(100 * time.Millisecond)
	defer r.StopCleanup()

	data := bytes.Repeat([]byte("y"), MaxFragmentPayloadSize*3+5)
	packets := EncodeRequest(9, 0, data)

	// Reverse the fragment order.
	for i, j := 0, len(packets)-1; i < j; i, j = i+1, j-1 {
		packets[i], packets[j] = packets[j], packets[i]
	}

	var full []byte
	for _, p := range packets {
		info, total, dsize, doff, frag, err := DecodeRequest(p)
		if err != nil {
			t.Fatalf("DecodeRequest: %v", err)
		}
		out, err := r.AddFragment("1.2.3.4", info.Fn, info.Stage, total, dsize, doff, frag)
		if err != nil {
			t.Fatalf("AddFragment: %v", err)
		}
		if out != nil {
			full = out
		}
	}
	if !bytes.Equal(full, data) {
		t.Fatalf("out-of-order reassembly failed: got %d bytes, want %d", len(full), len(data))
	}
}

// Two different commands in flight at once must not evict each other (fix for
// the single-currentMessage limitation).
func TestReassemblerInterleavedDifferentFn(t *testing.T) {
	r := NewReassembler(500 * time.Millisecond)
	defer r.StopCleanup()

	dataA := bytes.Repeat([]byte("A"), MaxFragmentPayloadSize+10)
	dataB := bytes.Repeat([]byte("B"), MaxFragmentPayloadSize+20)
	packetsA := EncodeRequest(1, 0, dataA)
	packetsB := EncodeRequest(2, 0, dataB)
	if len(packetsA) < 2 || len(packetsB) < 2 {
		t.Fatal("test requires multi-fragment payloads")
	}

	decode := func(p []byte) (PayloadInfo, uint32, uint32, uint32, []byte) {
		info, total, dsize, doff, frag, err := DecodeRequest(p)
		if err != nil {
			t.Fatalf("DecodeRequest: %v", err)
		}
		return info, total, dsize, doff, frag
	}

	// Interleave: A0, B0, A1, B1, ...
	var gotA, gotB []byte
	maxLen := len(packetsA)
	if len(packetsB) > maxLen {
		maxLen = len(packetsB)
	}
	for i := 0; i < maxLen; i++ {
		if i < len(packetsA) {
			info, total, dsize, doff, frag := decode(packetsA[i])
			out, err := r.AddFragment("1.2.3.4", info.Fn, info.Stage, total, dsize, doff, frag)
			if err != nil {
				t.Fatalf("AddFragment A: %v", err)
			}
			if out != nil {
				gotA = out
			}
		}
		if i < len(packetsB) {
			info, total, dsize, doff, frag := decode(packetsB[i])
			out, err := r.AddFragment("1.2.3.4", info.Fn, info.Stage, total, dsize, doff, frag)
			if err != nil {
				t.Fatalf("AddFragment B: %v", err)
			}
			if out != nil {
				gotB = out
			}
		}
	}

	if !bytes.Equal(gotA, dataA) {
		t.Fatalf("fn=1 stream corrupted: got %d bytes, want %d", len(gotA), len(dataA))
	}
	if !bytes.Equal(gotB, dataB) {
		t.Fatalf("fn=2 stream corrupted: got %d bytes, want %d", len(gotB), len(dataB))
	}
}

func TestReassemblerEmptyMessage(t *testing.T) {
	r := NewReassembler(100 * time.Millisecond)
	defer r.StopCleanup()

	full, err := r.AddFragment("1.2.3.4", 1, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("AddFragment error: %v", err)
	}
	if full == nil || len(full) != 0 {
		t.Fatalf("empty message should return empty non-nil slice, got %v", full)
	}
}

func TestReassemblerTimeoutCleanup(t *testing.T) {
	// Long timeout so the ticker (timeout/2) does not fire during the asserts,
	// then a short wait to let cleanup clear a stale message.
	r := NewReassembler(40 * time.Millisecond)
	defer r.StopCleanup()

	// Send only the first fragment of a two-fragment message: never completes.
	data := bytes.Repeat([]byte("z"), MaxFragmentPayloadSize+10)
	packets := EncodeRequest(3, 0, data)
	info, total, dsize, doff, frag, err := DecodeRequest(packets[0])
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if out, err := r.AddFragment("1.2.3.4", info.Fn, info.Stage, total, dsize, doff, frag); err != nil || out != nil {
		t.Fatalf("unexpected result: out=%v err=%v", out, err)
	}

	// Wait beyond the timeout + one cleanup tick.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.RLock()
		n := len(r.messages)
		r.mu.RUnlock()
		if n == 0 {
			return // cleaned up as expected
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stale partial message was not cleaned up after timeout")
}

func TestReassemblerClear(t *testing.T) {
	r := NewReassembler(500 * time.Millisecond)
	defer r.StopCleanup()

	data := bytes.Repeat([]byte("q"), MaxFragmentPayloadSize+10)
	packets := EncodeRequest(4, 0, data)
	info, total, dsize, doff, frag, _ := DecodeRequest(packets[0])
	if _, err := r.AddFragment("1.2.3.4", info.Fn, info.Stage, total, dsize, doff, frag); err != nil {
		t.Fatalf("AddFragment: %v", err)
	}

	r.Clear()

	r.mu.RLock()
	n := len(r.messages)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("Clear left %d in-flight messages, want 0", n)
	}
}
