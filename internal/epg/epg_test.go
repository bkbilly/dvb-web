package epg

import (
	"testing"

	"dvb-web/internal/db"
)

func TestDecodeDVBStringGreek(t *testing.T) {
	// 1. Raw ISO-8859-7 without prefix: "ΕΡΤ 1."
	raw := []byte{0xc5, 0xd1, 0xd4, 0x20, 0x31, 0x2e}
	got := DecodeDVBString(raw)
	expected := "ΕΡΤ 1."
	if got != expected {
		t.Errorf("DecodeDVBString raw ISO-8859-7 = %q; want %q", got, expected)
	}

	// 2. ISO-8859-7 with 0x03 prefix
	withPrefix := append([]byte{0x03}, raw...)
	got = DecodeDVBString(withPrefix)
	if got != expected {
		t.Errorf("DecodeDVBString with 0x03 prefix = %q; want %q", got, expected)
	}

	// 3. ISO-8859-7 with 0x10 0x00 0x07 prefix
	with3Byte := append([]byte{0x10, 0x00, 0x07}, raw...)
	got = DecodeDVBString(with3Byte)
	if got != expected {
		t.Errorf("DecodeDVBString with 0x10 0x00 0x07 = %q; want %q", got, expected)
	}

	// 4. UTF-8 with 0x15 prefix
	utf8Bytes := append([]byte{0x15}, []byte("ΕΡΤ 1.")...)
	got = DecodeDVBString(utf8Bytes)
	if got != expected {
		t.Errorf("DecodeDVBString UTF-8 = %q; want %q", got, expected)
	}

	// 5. Greek text with 0x8A newline
	greekWithNL := []byte{0xc5, 0xd1, 0xd4, 0x8a, 0x31}
	got = DecodeDVBString(greekWithNL)
	if got != "ΕΡΤ\n1" {
		t.Errorf("DecodeDVBString with 0x8A newline = %q; want %q", got, "ΕΡΤ\n1")
	}
}

func TestProcessEITExtendedEventItems(t *testing.T) {
	// Build an Extended Event Descriptor (0x4E) with Artist and Track items
	// Header: desc_num (0x00), lang: "eng" (3 bytes), length_of_items (1 byte)
	item1Desc := []byte("Artist")
	item1Val := []byte("Queen")
	item2Desc := []byte("Track")
	item2Val := []byte("Bohemian Rhapsody")

	var itemsBytes []byte
	itemsBytes = append(itemsBytes, byte(len(item1Desc)))
	itemsBytes = append(itemsBytes, item1Desc...)
	itemsBytes = append(itemsBytes, byte(len(item1Val)))
	itemsBytes = append(itemsBytes, item1Val...)

	itemsBytes = append(itemsBytes, byte(len(item2Desc)))
	itemsBytes = append(itemsBytes, item2Desc...)
	itemsBytes = append(itemsBytes, byte(len(item2Val)))
	itemsBytes = append(itemsBytes, item2Val...)

	var extDesc []byte
	extDesc = append(extDesc, 0x4E) // Tag
	descPayloadLen := 1 + 3 + 1 + len(itemsBytes) + 1 + 0 // descriptor_num + lang + length_of_items + items + text_length + text
	extDesc = append(extDesc, byte(descPayloadLen))
	extDesc = append(extDesc, 0x00) // descriptor_number / last_descriptor_number
	extDesc = append(extDesc, []byte("eng")...)
	extDesc = append(extDesc, byte(len(itemsBytes)))
	extDesc = append(extDesc, itemsBytes...)
	extDesc = append(extDesc, 0x00) // text_length = 0

	// Event block:
	// event_id (2) + start_time MJD BCD (5) + duration BCD (3) + descriptors_loop_length (2) + descriptors
	var eventBlock []byte
	eventBlock = append(eventBlock, 0x00, 0x01) // event_id = 1
	// MJD for year 2026: 0xEE, 0x71 (approx), Time 12:00:00: 0x12, 0x00, 0x00
	eventBlock = append(eventBlock, 0xEE, 0x71, 0x12, 0x00, 0x00)
	// Duration: 1h -> 0x01, 0x00, 0x00
	eventBlock = append(eventBlock, 0x01, 0x00, 0x00)
	// Descriptors loop length:
	eventBlock = append(eventBlock, byte(len(extDesc)>>8), byte(len(extDesc)&0xFF))
	eventBlock = append(eventBlock, extDesc...)

	// EIT Section
	// Header:
	// table_id: 0x4E (1)
	// section_syntax_indicator & section_length (2)
	// service_id: 0x00, 0x65 (101) (2)
	// version / cni: 0xC1 (1)
	// section_number: 0x00 (1)
	// last_section_number: 0x00 (1)
	// transport_stream_id: 0x00, 0x01 (2)
	// original_network_id: 0x00, 0x01 (2)
	// segment_last_section_number: 0x00 (1)
	// last_table_id: 0x4E (1)
	// Total header before payload = 14 bytes
	sectionPayload := append(eventBlock, 0x00, 0x00, 0x00, 0x00) // + 4 bytes CRC
	sectionLength := 11 + len(sectionPayload) // section_length starts after section_length field (byte 3)

	var sec []byte
	sec = append(sec, 0x4E) // table_id
	sec = append(sec, byte(0xB0|((sectionLength>>8)&0x0F)), byte(sectionLength&0xFF))
	sec = append(sec, 0x00, 0x65) // service_id = 101
	sec = append(sec, 0xC1, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x4E)
	sec = append(sec, sectionPayload...)

	var capturedEvents []db.EPGEvent
	parser := NewParser(nil, func(events []db.EPGEvent) {
		capturedEvents = append(capturedEvents, events...)
	})

	channelMap := map[int]int64{101: 42}
	parser.processEITSection(sec, channelMap)

	if len(capturedEvents) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(capturedEvents))
	}
	ev := capturedEvents[0]
	if ev.ChannelID != 42 {
		t.Errorf("Expected ChannelID 42, got %d", ev.ChannelID)
	}
	if ev.Titles["eng"] != "Bohemian Rhapsody" {
		t.Errorf("Expected Title 'Bohemian Rhapsody' derived from Track item, got %q", ev.Titles["eng"])
	}
	expectedDesc := "Artist: Queen - Track: Bohemian Rhapsody"
	if ev.Descriptions["eng"] != expectedDesc {
		t.Errorf("Expected Description %q, got %q", expectedDesc, ev.Descriptions["eng"])
	}
}
