package transcoder

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type HardwareType string

const (
	HW_NONE    HardwareType = "cpu"
	HW_VAAPI   HardwareType = "vaapi"
	HW_NVENC   HardwareType = "nvenc"
	HW_V4L2M2M HardwareType = "v4l2m2m"
)

type Detector struct {
	mu          sync.RWMutex
	detectedHW  HardwareType
	vaapiDevice string
	ffmpegPath  string
}

func NewDetector() *Detector {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		ffmpegPath = "ffmpeg"
	}

	d := &Detector{
		ffmpegPath:  ffmpegPath,
		detectedHW:  HW_NONE,
		vaapiDevice: "/dev/dri/renderD128",
	}
	d.Detect()
	return d
}

func (d *Detector) Detect() HardwareType {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 1. Check NVIDIA NVENC (GeForce / Quadro / Tesla GPUs)
	if d.checkFFmpegEncoder("h264_nvenc") && d.testEncoder("h264_nvenc", nil) {
		d.detectedHW = HW_NVENC
		log.Printf("[Transcoder] Detected and verified NVIDIA NVENC hardware acceleration")
		return HW_NVENC
	}

	// 2. Check Intel/AMD VA-API with real device test
	if _, err := os.Stat(d.vaapiDevice); err == nil {
		vaapiArgs := []string{"-init_hw_device", fmt.Sprintf("vaapi=va:%s", d.vaapiDevice), "-filter_hw_device", "va"}
		if d.checkFFmpegEncoder("h264_vaapi") && d.testEncoder("h264_vaapi", vaapiArgs) {
			d.detectedHW = HW_VAAPI
			log.Printf("[Transcoder] Detected and verified VA-API hardware acceleration at %s", d.vaapiDevice)
			return HW_VAAPI
		}
	}

	// 3. Check Raspberry Pi / ARM V4L2 M2M
	if _, err := os.Stat("/dev/video10"); err == nil {
		if d.checkFFmpegEncoder("h264_v4l2m2m") {
			d.detectedHW = HW_V4L2M2M
			log.Printf("[Transcoder] Detected V4L2 M2M hardware acceleration")
			return HW_V4L2M2M
		}
	}

	// 4. Fallback to CPU
	d.detectedHW = HW_NONE
	log.Printf("[Transcoder] Using CPU encoding (libx264 ultrafast zerolatency)")
	return HW_NONE
}

func (d *Detector) testEncoder(encoderName string, extraArgs []string) bool {
	args := []string{"-hide_banner", "-loglevel", "error"}
	if len(extraArgs) > 0 {
		args = append(args, extraArgs...)
	}
	args = append(args, "-f", "lavfi", "-i", "nullsrc=s=320x240", "-frames:v", "1", "-c:v", encoderName, "-f", "null", "-")

	cmd := exec.Command(d.ffmpegPath, args...)
	return cmd.Run() == nil
}

func (d *Detector) GetActiveHardware() HardwareType {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.detectedHW
}

func (d *Detector) checkFFmpegEncoder(encoderName string) bool {
	cmd := exec.Command(d.ffmpegPath, "-hide_banner", "-encoders")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), encoderName)
}

// PipelineConfig contains the parameters needed to launch an FFmpeg transcode session
type PipelineConfig struct {
	InputPath      string       // e.g. "/dev/dvb/adapter0/dvr0" or pipe
	InputPipe      io.Reader    // Piped TS reader (preferred for safe multi-consumer fanning)
	IsMock         bool         // If mock, generates test pattern
	ServiceID      int          // DVB Program / Service ID
	VideoUDPPort   int          // Local UDP port to stream RTP H.264
	AudioUDPPort   int          // Local UDP port to stream RTP Opus
	HWType         HardwareType // Selected HW type (or auto)
	VideoBitrate   string       // e.g. "2500k"
	AVSyncOffsetMs int          // Calibrated audio delay to align with video pipeline
}

type Pipeline struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdout io.ReadCloser
}

func (p *Pipeline) Stdout() io.ReadCloser {
	return p.stdout
}

func (d *Detector) StartPipeline(cfg PipelineConfig) (*Pipeline, error) {
	ctx, cancel := context.WithCancel(context.Background())

	hw := cfg.HWType
	if hw == "" || hw == "auto" {
		hw = d.GetActiveHardware()
	}

	bitrate := cfg.VideoBitrate
	if bitrate == "" {
		bitrate = "2500k"
	}

	var args []string
	args = append(args, "-hide_banner")
	if cfg.InputPipe == nil {
		args = append(args, "-nostdin")
	}
	args = append(args, "-loglevel", "warning")

	// Hardware input initialization for VA-API
	if !cfg.IsMock && hw == HW_VAAPI {
		args = append(args,
			"-hwaccel", "vaapi",
			"-hwaccel_device", d.vaapiDevice,
			"-hwaccel_output_format", "vaapi",
		)
	}

	// Live MPEG-TS input options to ensure PAT/PMT program maps are properly discovered
	if !cfg.IsMock {
		args = append(args,
			"-analyzeduration", "800000",
			"-probesize", "1500000",
			"-err_detect", "ignore_err",
			"-fflags", "+genpts+discardcorrupt+nobuffer",
			"-flags", "low_delay",
		)
	}

	// Input source
	if cfg.IsMock {
		args = append(args,
			"-f", "lavfi", "-re", "-i", "testsrc=size=1280x720:rate=25",
			"-f", "lavfi", "-re", "-i", "sine=frequency=1000:sample_rate=48000",
		)
	} else if cfg.InputPipe != nil {
		args = append(args, "-i", "pipe:0")
	} else {
		args = append(args, "-i", cfg.InputPath)
	}

	// Stream mapping: multiplex Video and Audio together into one single stream
	if cfg.IsMock {
		args = append(args, "-map", "0:v:0", "-map", "1:a:0")
	} else if cfg.ServiceID > 0 {
		args = append(args,
			"-map", fmt.Sprintf("0:p:%d:v:0", cfg.ServiceID),
			"-map", fmt.Sprintf("0:p:%d:a:0?", cfg.ServiceID),
		)
	} else {
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?")
	}

	// Video filter chain: aspect ratio correction + de-interlace
	if !cfg.IsMock && hw != HW_VAAPI {
		args = append(args, "-vf", "yadif=mode=0:parity=auto:deint=interlaced,scale=w=trunc(ih*dar/2)*2:h=ih,setsar=1")
	}

	switch hw {
	case HW_NVENC:
		args = append(args,
			"-c:v", "h264_nvenc",
			"-preset", "p1",
			"-tune", "ull",
			"-zerolatency", "1",
			"-b:v", bitrate,
			"-g", "25",
			"-bf", "0",
			"-pix_fmt", "yuv420p",
		)
	case HW_VAAPI:
		args = append(args,
			"-c:v", "h264_vaapi",
			"-b:v", bitrate,
			"-g", "25",
			"-bf", "0",
		)
	case HW_V4L2M2M:
		args = append(args,
			"-c:v", "h264_v4l2m2m",
			"-b:v", bitrate,
			"-g", "25",
		)
	default:
		// Universal CPU fallback
		args = append(args,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-b:v", bitrate,
			"-g", "25",
			"-bf", "0",
			"-pix_fmt", "yuv420p",
		)
	}

	// Unified MPEG-TS output with AAC stereo audio (natural PTS preserved for perfect A/V sync)
	args = append(args,
		"-c:a", "aac",
		"-b:a", "128k",
		"-ar", "48000",
		"-ac", "2",
		"-f", "mpegts",
		"-pes_payload_size", "0",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, d.ffmpegPath, args...)
	if !cfg.IsMock && cfg.InputPipe != nil {
		cmd.Stdin = cfg.InputPipe
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	log.Printf("[Transcoder] Started unified A/V MPEG-TS pipeline (HW: %s, ServiceID: %d)", hw, cfg.ServiceID)

	return &Pipeline{
		cmd:    cmd,
		cancel: cancel,
		stdout: stdout,
	}, nil
}

func (p *Pipeline) Stop() {
	if p == nil {
		return
	}
	p.cancel()
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	log.Printf("[Transcoder] Pipeline stopped")
}
