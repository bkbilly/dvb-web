package epg

import (
	"testing"
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
