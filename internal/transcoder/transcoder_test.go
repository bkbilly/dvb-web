package transcoder

import (
	"strings"
	"testing"
)

func TestSafeBuffer(t *testing.T) {
	buf := &safeBuffer{max: 100}
	buf.Write([]byte(strings.Repeat("a", 80)))
	if len(buf.String()) != 80 {
		t.Fatalf("expected 80 bytes, got %d", len(buf.String()))
	}

	// Write more than max to verify ring-buffer truncation
	buf.Write([]byte(strings.Repeat("b", 40)))
	res := buf.String()
	if len(res) > 100 {
		t.Fatalf("expected buffer <= 100 bytes, got %d", len(res))
	}
	if !strings.HasSuffix(res, strings.Repeat("b", 40)) {
		t.Fatalf("expected suffix of 'b's")
	}
}

func TestDetectorFallbackOnFailedHW(t *testing.T) {
	d := NewDetector()
	d.vaapiDevice = "/dev/nonexistent_hw_device"
	// Set active HW to VAAPI
	d.mu.Lock()
	d.detectedHW = HW_VAAPI
	d.mu.Unlock()

	cfg := PipelineConfig{
		IsMock:  true,
		HWType:  HW_VAAPI,
		IsRadio: false,
	}

	p, err := d.StartPipeline(cfg)
	if err != nil {
		t.Fatalf("expected pipeline to fall back to CPU and succeed, but failed: %v", err)
	}
	defer p.Stop()

	// Verify that detectedHW was demoted to HW_NONE
	if d.GetActiveHardware() != HW_NONE {
		t.Fatalf("expected active hardware to be demoted to HW_NONE, got %s", d.GetActiveHardware())
	}
}
