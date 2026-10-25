package tuner

import (
	"encoding/binary"
	"testing"
)

func makeTestTSPacket(pid int, pusi bool, payload []byte) []byte {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	pkt[1] = byte((pid >> 8) & 0x1F)
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[2] = byte(pid & 0xFF)
	pkt[3] = 0x10 // payload only, CC=0
	copy(pkt[4:], payload)
	for i := 4 + len(payload); i < 188; i++ {
		pkt[i] = 0xFF
	}
	return pkt
}

func makeTestPAT(tsID int, programs map[int]int) []byte {
	var progBytes []byte
	for sid, pmt := range programs {
		p := make([]byte, 4)
		binary.BigEndian.PutUint16(p[0:2], uint16(sid))
		binary.BigEndian.PutUint16(p[2:4], uint16(pmt|0xE000))
		progBytes = append(progBytes, p...)
	}
	secLen := 5 + len(progBytes) + 4
	sec := make([]byte, 3+secLen)
	sec[0] = 0x00
	binary.BigEndian.PutUint16(sec[1:3], uint16(secLen|0xB000))
	binary.BigEndian.PutUint16(sec[3:5], uint16(tsID))
	sec[5] = 0xC1
	sec[6] = 0x00
	sec[7] = 0x00
	copy(sec[8:], progBytes)
	return sec
}

type testServiceInfo struct {
	sid      int
	sType    int
	provider string
	name     string
}

func makeTestSDT(tableID byte, tsID int, services []testServiceInfo) []byte {
	var descBytes []byte
	for _, s := range services {
		sBuf := make([]byte, 5)
		binary.BigEndian.PutUint16(sBuf[0:2], uint16(s.sid))
		sBuf[2] = 0xFC

		dData := []byte{byte(s.sType), byte(len(s.provider))}
		dData = append(dData, []byte(s.provider)...)
		dData = append(dData, byte(len(s.name)))
		dData = append(dData, []byte(s.name)...)

		descLen := 2 + len(dData)
		binary.BigEndian.PutUint16(sBuf[3:5], uint16(descLen&0x0FFF))

		sBuf = append(sBuf, 0x48, byte(len(dData)))
		sBuf = append(sBuf, dData...)
		descBytes = append(descBytes, sBuf...)
	}

	secLen := 8 + len(descBytes) + 4
	sec := make([]byte, 3+secLen)
	sec[0] = tableID
	binary.BigEndian.PutUint16(sec[1:3], uint16(secLen|0xF000))
	binary.BigEndian.PutUint16(sec[3:5], uint16(tsID))
	sec[5] = 0xC1
	sec[6] = 0x00
	sec[7] = 0x00
	binary.BigEndian.PutUint16(sec[8:10], 0x0001)
	sec[10] = 0xFF
	copy(sec[11:], descBytes)
	return sec
}

func packetizeTestSection(pid int, sec []byte) []byte {
	var tsBytes []byte
	first := true
	offset := 0
	for offset < len(sec) {
		if first {
			payload := []byte{0x00} // pointer_field = 0
			chunkLen := 183
			if len(sec)-offset < chunkLen {
				chunkLen = len(sec) - offset
			}
			payload = append(payload, sec[offset:offset+chunkLen]...)
			offset += chunkLen
			tsBytes = append(tsBytes, makeTestTSPacket(pid, true, payload)...)
			first = false
		} else {
			chunkLen := 184
			if len(sec)-offset < chunkLen {
				chunkLen = len(sec) - offset
			}
			payload := sec[offset : offset+chunkLen]
			offset += chunkLen
			tsBytes = append(tsBytes, makeTestTSPacket(pid, false, payload)...)
		}
	}
	return tsBytes
}

func TestParseSDT_Table0x42_Accepted(t *testing.T) {
	services := []testServiceInfo{
		{sid: 10, sType: 1, provider: "ERT", name: "ERT 1 HD"},
		{sid: 20, sType: 1, provider: "ERT", name: "ERT 2 HD"},
	}
	sec := makeTestSDT(0x42, 1, services)

	names := make(map[int]string)
	types := make(map[int]int)
	parseSDT(sec, names, types)

	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[10] != "ERT 1 HD" {
		t.Errorf("expected ERT 1 HD, got %q", names[10])
	}
	if names[20] != "ERT 2 HD" {
		t.Errorf("expected ERT 2 HD, got %q", names[20])
	}
}

func TestParseSDT_Table0x46_Ignored(t *testing.T) {
	services := []testServiceInfo{
		{sid: 100, sType: 1, provider: "Digea", name: "MEGA HD"},
	}
	// Table ID 0x46 is SDT Other TS
	sec := makeTestSDT(0x46, 2, services)

	names := make(map[int]string)
	types := make(map[int]int)
	parseSDT(sec, names, types)

	if len(names) != 0 {
		t.Errorf("expected SDT other (0x46) to be ignored, but got %v", names)
	}
}

func TestScanDemuxer_MultiPacketSDT_AndSync(t *testing.T) {
	// Create multiplex with 6 channels (SDT will be > 188 bytes, spanning multiple TS packets)
	services := []testServiceInfo{
		{sid: 101, sType: 1, provider: "Broadcast Corp", name: "Channel 1 HD"},
		{sid: 102, sType: 1, provider: "Broadcast Corp", name: "Channel 2 HD"},
		{sid: 103, sType: 1, provider: "Broadcast Corp", name: "Channel 3 News"},
		{sid: 104, sType: 1, provider: "Broadcast Corp", name: "Channel 4 Cinema"},
		{sid: 105, sType: 1, provider: "Broadcast Corp", name: "Channel 5 Sports"},
		{sid: 106, sType: 2, provider: "Broadcast Corp", name: "Radio 1 National"},
	}

	patSec := makeTestPAT(1, map[int]int{
		101: 1010,
		102: 1020,
		103: 1030,
		104: 1040,
		105: 1050,
		106: 1060,
	})
	sdtActualSec := makeTestSDT(0x42, 1, services)

	// Also simulate SDT other (0x46) for another multiplex that should be ignored
	sdtOtherSec := makeTestSDT(0x46, 2, []testServiceInfo{
		{sid: 201, sType: 1, provider: "Other Mux", name: "Other Channel"},
	})

	var tsStream []byte
	// Prepend some unaligned junk bytes to verify 0x47 synchronization
	tsStream = append(tsStream, []byte{0x00, 0x12, 0x34, 0x56, 0x78}...)

	// Append PAT TS packets
	tsStream = append(tsStream, packetizeTestSection(0x0000, patSec)...)

	// Append SDT other packets (should be ignored)
	tsStream = append(tsStream, packetizeTestSection(0x0011, sdtOtherSec)...)

	// Append SDT actual multi-packet TS packets
	tsStream = append(tsStream, packetizeTestSection(0x0011, sdtActualSec)...)

	demuxer := newScanDemuxer()
	// Feed in arbitrary chunk sizes (e.g. 500 bytes at a time) to verify chunk resilience
	chunkSize := 500
	for i := 0; i < len(tsStream); i += chunkSize {
		end := i + chunkSize
		if end > len(tsStream) {
			end = len(tsStream)
		}
		demuxer.feedChunk(tsStream[i:end])
	}

	if !demuxer.isComplete() {
		t.Fatalf("expected demuxer to be complete with all 6 channels")
	}

	chNum := 1
	channels := demuxer.channels(506000000, "DVBT2", &chNum)

	if len(channels) != 6 {
		t.Fatalf("expected 6 channels, got %d", len(channels))
	}

	// Verify all channels were discovered correctly in sorted order
	expectedSIDs := []int{101, 102, 103, 104, 105, 106}
	expectedNames := []string{
		"Channel 1 HD",
		"Channel 2 HD",
		"Channel 3 News",
		"Channel 4 Cinema",
		"Channel 5 Sports",
		"Radio 1 National",
	}

	for i, ch := range channels {
		if ch.ServiceID != expectedSIDs[i] {
			t.Errorf("channel[%d] ServiceID = %d, expected %d", i, ch.ServiceID, expectedSIDs[i])
		}
		if ch.Name != expectedNames[i] {
			t.Errorf("channel[%d] Name = %q, expected %q", i, ch.Name, expectedNames[i])
		}
		if ch.Frequency != 506000000 {
			t.Errorf("channel[%d] Frequency = %d, expected 506000000", i, ch.Frequency)
		}
		if ch.DeliverySystem != "DVBT2" {
			t.Errorf("channel[%d] DeliverySystem = %s, expected DVBT2", i, ch.DeliverySystem)
		}
		if ch.ServiceID == 106 && !ch.IsRadio {
			t.Errorf("channel 106 should be radio")
		}
	}
}

func TestSanitizeAndSyncTS(t *testing.T) {
	// 1. Clean valid TS packets
	pkt1 := makeTestTSPacket(100, false, []byte("hello"))
	pkt2 := makeTestTSPacket(200, false, []byte("world"))
	data := append(pkt1, pkt2...)

	out, leftover := SanitizeAndSyncTS(data, nil)
	if len(leftover) != 0 {
		t.Fatalf("expected 0 leftover, got %d", len(leftover))
	}
	if len(out) != 188*2 {
		t.Fatalf("expected 376 bytes output, got %d", len(out))
	}
	if out[0] != 0x47 || out[188] != 0x47 {
		t.Errorf("sync bytes missing in output")
	}

	// 2. TEI (Transport Error Indicator) packet replacement with null packet
	corruptPkt := makeTestTSPacket(100, false, []byte("corrupt-data"))
	corruptPkt[1] |= 0x80 // Set TEI bit (simulating high bit error rate)
	validPkt := makeTestTSPacket(200, false, []byte("valid-data"))
	mixedData := append(corruptPkt, validPkt...)

	out, leftover = SanitizeAndSyncTS(mixedData, nil)
	if len(out) != 188*2 {
		t.Fatalf("expected 376 bytes output, got %d", len(out))
	}
	// Packet 0 should be replaced with MPEG-TS Null Packet (PID 0x1FFF, 0x47 0x1F 0xFF 0x10)
	if out[0] != 0x47 || out[1] != 0x1F || out[2] != 0xFF || out[3] != 0x10 {
		t.Errorf("TEI packet was not properly replaced with Null packet: %v", out[:4])
	}
	// Packet 1 should remain untouched valid packet
	if out[188] != 0x47 || (out[189]&0x1F) != byte((200>>8)&0x1F) || out[190] != byte(200&0xFF) {
		t.Errorf("valid packet was corrupted: %v", out[188:192])
	}

	// 3. Junk bytes preceding stream (misalignment recovery)
	junk := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x12, 0x34}
	junkStream := append(junk, pkt1...)
	junkStream = append(junkStream, pkt2...)

	out, leftover = SanitizeAndSyncTS(junkStream, nil)
	if len(out) != 188*2 {
		t.Fatalf("expected 376 bytes output after junk resync, got %d", len(out))
	}
	if out[0] != 0x47 || out[188] != 0x47 {
		t.Errorf("sync bytes missing after resync")
	}

	// 4. Incomplete packet leftover assembly
	partial := pkt1[:100]
	rest := append(pkt1[100:], pkt2...)

	out1, leftover1 := SanitizeAndSyncTS(partial, nil)
	if len(out1) != 0 {
		t.Fatalf("expected 0 output for partial packet, got %d", len(out1))
	}
	if len(leftover1) != 100 {
		t.Fatalf("expected 100 leftover bytes, got %d", len(leftover1))
	}

	out2, leftover2 := SanitizeAndSyncTS(rest, leftover1)
	if len(leftover2) != 0 {
		t.Fatalf("expected 0 leftover after reassembly, got %d", len(leftover2))
	}
	if len(out2) != 188*2 {
		t.Fatalf("expected 376 bytes output after reassembly, got %d", len(out2))
	}
	if out2[0] != 0x47 || out2[188] != 0x47 {
		t.Errorf("sync bytes missing after reassembly")
	}

	// 5. Corrupted sync byte in middle packet (packet 2 has 0x46 instead of 0x47)
	pkt3 := makeTestTSPacket(300, false, []byte("third"))
	badSyncPkt := append([]byte(nil), pkt2...)
	badSyncPkt[0] = 0x46 // bit flip in sync byte

	streamWithBadSync := append(append(append([]byte(nil), pkt1...), badSyncPkt...), pkt3...)

	out3, leftover3 := SanitizeAndSyncTS(streamWithBadSync, nil)
	if len(leftover3) != 0 {
		t.Fatalf("expected 0 leftover, got %d", len(leftover3))
	}
	if len(out3) != 188*2 {
		t.Fatalf("expected 376 bytes output (pkt1 and pkt3), got %d", len(out3))
	}
	if out3[0] != 0x47 || out3[188] != 0x47 {
		t.Errorf("sync bytes missing in output after bad sync byte")
	}
}
