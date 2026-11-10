package transcoder

import (
	"strings"
	"testing"
	"time"
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

func TestDetectorFallbackOnFailedNVENC(t *testing.T) {
	d := NewDetector()
	// Mark NVENC as demoted/unavailable (as happens when Docker/host GPU sleeps or cuInit fails)
	d.DemoteHardware(HW_NVENC)
	d.mu.Lock()
	d.detectedHW = HW_NVENC
	d.mu.Unlock()

	cfg := PipelineConfig{
		IsMock:  true,
		HWType:  HW_NVENC,
		IsRadio: false,
	}

	p, err := d.StartPipeline(cfg)
	if err != nil {
		t.Fatalf("expected pipeline to fall back to CPU and succeed, but failed: %v", err)
	}
	defer p.Stop()

	if p.HWType() != HW_NONE {
		t.Fatalf("expected pipeline HWType to be HW_NONE (CPU), got %s", p.HWType())
	}

	if d.GetActiveHardware() != HW_NONE {
		t.Fatalf("expected active hardware to be demoted to HW_NONE, got %s", d.GetActiveHardware())
	}
}

func TestDetectorHardwareValidation(t *testing.T) {
	d := NewDetector()
	if !d.IsHardwareAvailable(HW_NONE) {
		t.Fatalf("HW_NONE should always be available")
	}

	d.DemoteHardware(HW_NVENC)
	if d.IsHardwareAvailable(HW_NVENC) {
		t.Fatalf("HW_NVENC should not be available after demotion")
	}
}

func TestDetectorCacheTTLExpiration(t *testing.T) {
	d := NewDetector()
	// Simulate an entry that was checked 2 hours ago (idle server)
	d.mu.Lock()
	if d.verifiedHW == nil {
		d.verifiedHW = make(map[HardwareType]hwStatus)
	}
	d.verifiedHW[HW_NVENC] = hwStatus{
		available: true,
		checkedAt: time.Now().Add(-2 * time.Hour),
	}
	d.mu.Unlock()

	// Calling IsHardwareAvailable will notice the cache is expired (>60s)
	// and re-evaluate live using testHardwareLocked.
	// Since testHardwareLocked checks real ffmpeg/nvenc, it correctly re-probes instead of using stale state.
	avail := d.IsHardwareAvailable(HW_NVENC)
	d.mu.RLock()
	st := d.verifiedHW[HW_NVENC]
	d.mu.RUnlock()

	if time.Since(st.checkedAt) > 5*time.Second {
		t.Fatalf("expected checkedAt to be refreshed recently, got %v", st.checkedAt)
	}
	if st.available != avail {
		t.Fatalf("expected cached status to match return value")
	}
}

func TestDirectStreamFallbackWithSubtitles(t *testing.T) {
	d := NewDetector()
	cfg := PipelineConfig{
		IsMock:        true,
		HWType:        HW_DIRECT,
		SubtitleTrack: 0,
		IsRadio:       false,
	}

	p, err := d.StartPipeline(cfg)
	if err != nil {
		t.Fatalf("expected pipeline to start with fallback: %v", err)
	}
	defer p.Stop()

	// Direct stream cannot burn subtitles; it must have fallen back to transcoding (not HW_DIRECT)
	if p.HWType() == HW_DIRECT {
		t.Fatalf("expected pipeline to fall back from HW_DIRECT when subtitles enabled, got HW_DIRECT")
	}
}

func TestDirectStreamFallbackWithMPEG2(t *testing.T) {
	d := NewDetector()
	cfg := PipelineConfig{
		IsMock:        true,
		HWType:        HW_DIRECT,
		SubtitleTrack: -1,
		VideoCodec:    "mpeg2video",
		IsRadio:       false,
	}

	p, err := d.StartPipeline(cfg)
	if err != nil {
		t.Fatalf("expected pipeline to start with fallback: %v", err)
	}
	defer p.Stop()

	// Direct stream cannot output MPEG-2 to HTML5 MSE; it must have fallen back to transcoding
	if p.HWType() == HW_DIRECT {
		t.Fatalf("expected pipeline to fall back from HW_DIRECT for MPEG-2 video, got HW_DIRECT")
	}
}

func TestVisualRadioPipeline(t *testing.T) {
	d := NewDetector()
	// Visual radio has IsRadio: true, but VideoPID > 0 (or HasVideo: true).
	// It should preserve the broadcast video slide (0:p:SID:v:0?) and NOT generate a synthetic blue canvas.
	cfg := PipelineConfig{
		InputPath:     "/dev/null",
		IsRadio:       true,
		VideoPID:      32,
		ServiceID:     33,
		SubtitleTrack: -1,
		HWType:        HW_NONE,
	}

	args := d.BuildPipelineArgs(cfg, HW_NONE)
	argsJoined := strings.Join(args, " ")
	if strings.Contains(argsJoined, "color=c=0x0f172a") {
		t.Fatalf("visual radio pipeline should not use synthetic color background, got args: %s", argsJoined)
	}
	if !strings.Contains(argsJoined, "0:p:33:v:0?") {
		t.Fatalf("visual radio pipeline should map broadcast video slide stream 0:p:33:v:0?, got args: %s", argsJoined)
	}
	if !strings.Contains(argsJoined, "fps=fps=25") {
		t.Fatalf("visual radio pipeline should repeat still-picture frames at steady 25fps, got args: %s", argsJoined)
	}
	if !strings.Contains(argsJoined, "-analyzeduration 8000000") || !strings.Contains(argsJoined, "-probesize 10000000") {
		t.Fatalf("visual radio pipeline should use extended probe parameters for MPEG-TS, got args: %s", argsJoined)
	}
}

func TestAudioOnlyRadioPipeline(t *testing.T) {
	d := NewDetector()
	// Audio-only radio has IsRadio: true and VideoPID == 0.
	// It should synthesize a lightweight color background for browser playback.
	cfg := PipelineConfig{
		InputPath:     "/dev/null",
		IsRadio:       true,
		VideoPID:      0,
		ServiceID:     33,
		SubtitleTrack: -1,
		HWType:        HW_NONE,
	}

	args := d.BuildPipelineArgs(cfg, HW_NONE)
	argsJoined := strings.Join(args, " ")
	if !strings.Contains(argsJoined, "color=c=0x0f172a") {
		t.Fatalf("audio-only radio pipeline should synthesize color canvas, got args: %s", argsJoined)
	}
}
