package hls

import (
	"testing"
)

func TestIsVideoKeyframe(t *testing.T) {
	// Not an MPEG-TS packet
	if isVideoKeyframe([]byte{0x00, 0x01}) {
		t.Fatalf("expected false for invalid packet")
	}

	// Normal TS packet without PUSI
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	pkt[1] = 0x01
	pkt[2] = 0x00
	pkt[3] = 0x10
	if isVideoKeyframe(pkt) {
		t.Fatalf("expected false when PUSI is not set")
	}

	// Packet with PUSI and Video PES with H.264 SPS (NAL type 7)
	pkt[1] = 0x41 // PUSI set, PID 0x0100
	// Payload at offset 4: PES header 00 00 01 E0, len 00 00, flags 80 C0, header len 0A, ...
	payload := []byte{
		0x00, 0x00, 0x01, 0xE0, // PES start code + video stream ID
		0x00, 0x00,             // PES packet length
		0x80, 0x00, 0x00,       // flags
		0x00, 0x00, 0x01, 0x67, // NAL start code + SPS (0x67 & 0x1F = 7)
	}
	copy(pkt[4:], payload)

	if !isVideoKeyframe(pkt) {
		t.Fatalf("expected true for video PES packet with SPS keyframe")
	}
}
