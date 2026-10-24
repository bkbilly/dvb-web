package tuner

import (
	"encoding/hex"
	"testing"
)

func TestParseAITSection(t *testing.T) {
	// Raw broadcast AIT section captured live from DVB-T
	rawHex := "74f0760010c10000f000f0690000005c000201f060000a050000010101ff010304010a67720006736b616973641512696e6465782e7068703f733d736b61697364160100022800030323687474703a2f2f736b61692e736d6172742d74762d646174612e636f6d2f686f6d652f0002050001047f53d910a0b3"
	sec, err := hex.DecodeString(rawHex)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}

	url := ParseAITSection(sec)
	expected := "http://skai.smart-tv-data.com/home/index.php?s=skaisd"
	if url != expected {
		t.Errorf("ParseAITSection() = %q; expected %q", url, expected)
	}

	// Test invalid / truncated / non-AIT data
	if got := ParseAITSection([]byte{0x00, 0x01, 0x02}); got != "" {
		t.Errorf("expected empty string for non-AIT table, got %q", got)
	}
	if got := ParseAITSection([]byte{0x74, 0x00}); got != "" {
		t.Errorf("expected empty string for truncated AIT table, got %q", got)
	}
}
