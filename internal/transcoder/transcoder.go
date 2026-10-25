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
	"syscall"
	"time"
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
	IsRadio        bool         // True for audio-only radio channels (no video stream in DVB)
	ServiceID      int          // DVB Program / Service ID
	VideoUDPPort   int          // Local UDP port to stream RTP H.264
	AudioUDPPort   int          // Local UDP port to stream RTP Opus
	AudioTrackIdx  int          // Selected audio stream index (0, 1, 2...)
	SubtitleTrack  int          // Selected subtitle stream index (-1 = off, 0, 1...)
	SubtitleOffsetMs int        // Subtitle timestamp offset in milliseconds (negative advances subtitles)
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
			"-analyzeduration", "1500000",
			"-probesize", "2500000",
			"-fpsprobesize", "0",
			"-err_detect", "crccheck",
			"-fflags", "+genpts+nobuffer",
			"-ec", "favor_inter+deblock",
			"-flags", "low_delay",
		)
	}

	// Input source
	if cfg.IsMock {
		if cfg.IsRadio {
			args = append(args,
				"-f", "lavfi", "-re", "-i", "color=c=0x0f172a:s=1280x720:r=10",
				"-f", "lavfi", "-re", "-i", "sine=frequency=440:sample_rate=48000",
			)
		} else {
			args = append(args,
				"-f", "lavfi", "-re", "-i", "testsrc=size=1280x720:rate=25",
				"-f", "lavfi", "-re", "-i", "sine=frequency=1000:sample_rate=48000",
			)
		}
	} else if cfg.InputPipe != nil {
		args = append(args, "-i", "pipe:0")
	} else {
		args = append(args, "-i", cfg.InputPath)
	}

	// Stream mapping: multiplex Video and Audio together into one single stream
	audioIdx := cfg.AudioTrackIdx
	if audioIdx < 0 {
		audioIdx = 0
	}
	subIdx := cfg.SubtitleTrack

	if cfg.IsMock {
		if cfg.IsRadio {
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
		} else if subIdx >= 0 {
			args = append(args,
				"-filter_complex", "[0:v]drawtext=text='[DVB Live Subtitles - CC]':fontcolor=white:fontsize=28:box=1:boxcolor=black@0.6:boxborderw=6:x=(w-text_w)/2:y=h-80[v]",
				"-map", "[v]",
				"-map", "1:a:0",
			)
		} else {
			args = append(args, "-map", "0:v:0", "-map", "1:a:0")
		}
	} else if cfg.IsRadio {
		// Radio station (audio-only): synthesize a lightweight static video background
		// so browser <video> engines maintain flawless timing and synchronized playback
		audioMap := fmt.Sprintf("0:a:%d?", audioIdx)
		if cfg.ServiceID > 0 {
			audioMap = fmt.Sprintf("0:p:%d:a:%d?", cfg.ServiceID, audioIdx)
		}
		args = append(args,
			"-filter_complex", "color=c=0x0f172a:s=1280x720:r=10[v]",
			"-map", "[v]",
			"-map", audioMap,
		)
	} else if cfg.ServiceID > 0 {
		audioMap := fmt.Sprintf("0:p:%d:a:%d?", cfg.ServiceID, audioIdx)
		if subIdx >= 0 {
			subOffsetSec := float64(cfg.SubtitleOffsetMs) / 1000.0
			subPtsChain := ""
			if subOffsetSec != 0 {
				subPtsChain = fmt.Sprintf("setpts=PTS+(%.3f)/TB,", subOffsetSec)
			}
			filter := fmt.Sprintf(
				"[0:p:%d:v:0]bwdif=mode=1:parity=auto:deint=interlaced,scale=w=trunc(ih*dar/2)*2:h=ih:flags=lanczos,setsar=1[main];"+
					"[0:p:%d:s:%d]%sformat=yuva420p[sub];"+
					"[main][sub]overlay=x='if(eq(w,W),0,(W-w)/2)':y='if(eq(h,H),0,H-h-40)':eof_action=pass:repeatlast=0[outv]",
				cfg.ServiceID, cfg.ServiceID, subIdx, subPtsChain,
			)
			args = append(args,
				"-filter_complex", filter,
				"-map", "[outv]",
				"-map", audioMap,
			)
		} else {
			if hw != HW_VAAPI {
				args = append(args, "-vf", "bwdif=mode=1:parity=auto:deint=interlaced,scale=w=trunc(ih*dar/2)*2:h=ih:flags=lanczos,setsar=1")
			}
			args = append(args,
				"-map", fmt.Sprintf("0:p:%d:v:0?", cfg.ServiceID),
				"-map", audioMap,
			)
		}
	} else {
		if subIdx >= 0 {
			subOffsetSec := float64(cfg.SubtitleOffsetMs) / 1000.0
			subPtsChain := ""
			if subOffsetSec != 0 {
				subPtsChain = fmt.Sprintf("setpts=PTS+(%.3f)/TB,", subOffsetSec)
			}
			filter := fmt.Sprintf(
				"[0:v:0]bwdif=mode=1:parity=auto:deint=interlaced,scale=w=trunc(ih*dar/2)*2:h=ih:flags=lanczos,setsar=1[main];"+
					"[0:s:%d]%sformat=yuva420p[sub];"+
					"[main][sub]overlay=x='if(eq(w,W),0,(W-w)/2)':y='if(eq(h,H),0,H-h-40)':eof_action=pass:repeatlast=0[outv]",
				subIdx, subPtsChain,
			)
			args = append(args,
				"-filter_complex", filter,
				"-map", "[outv]",
				"-map", fmt.Sprintf("0:a:%d?", audioIdx),
			)
		} else {
			if hw != HW_VAAPI {
				args = append(args, "-vf", "bwdif=mode=1:parity=auto:deint=interlaced,scale=w=trunc(ih*dar/2)*2:h=ih:flags=lanczos,setsar=1")
			}
			args = append(args,
				"-map", "0:v:0?",
				"-map", fmt.Sprintf("0:a:%d?", audioIdx),
			)
		}
	}

	if cfg.IsRadio {
		// Radio uses minimal CPU static frame encoding (10 fps, ~15 kbps)
		args = append(args,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "stillimage",
			"-b:v", "64k",
			"-g", "25",
			"-bf", "0",
			"-pix_fmt", "yuv420p",
		)
	} else {
		switch hw {
		case HW_NVENC:
			args = append(args,
				"-c:v", "h264_nvenc",
				"-preset", "p1",
				"-tune", "ull",
				"-zerolatency", "1",
				"-b:v", bitrate,
				"-g", "15",
				"-bf", "0",
				"-pix_fmt", "yuv420p",
			)
		case HW_VAAPI:
			args = append(args,
				"-c:v", "h264_vaapi",
				"-b:v", bitrate,
				"-g", "15",
				"-bf", "0",
			)
		case HW_V4L2M2M:
			args = append(args,
				"-c:v", "h264_v4l2m2m",
				"-b:v", bitrate,
				"-g", "15",
			)
		default:
			// Universal CPU fallback
			args = append(args,
				"-c:v", "libx264",
				"-preset", "veryfast",
				"-tune", "zerolatency",
				"-b:v", bitrate,
				"-maxrate", bitrate,
				"-bufsize", bitrate,
				"-g", "15",
				"-bf", "0",
				"-pix_fmt", "yuv420p",
			)
		}
	}

	audioBitrate := "128k"
	if cfg.IsRadio {
		audioBitrate = "192k" // Higher fidelity for radio music
	}

	// Unified MPEG-TS output with AAC stereo audio (natural PTS preserved for perfect A/V sync)
	args = append(args,
		"-c:a", "aac",
		"-b:a", audioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-af", "aresample=async=1000",
		"-f", "mpegts",
		"-muxdelay", "0",
		"-muxpreload", "0",
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

	// For subtitle pipelines, quickly ensure filtergraph didn't exit immediately on missing/unsupported stream
	if !cfg.IsMock && cfg.SubtitleTrack >= 0 {
		time.Sleep(300 * time.Millisecond)
		if cmd.Process != nil {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WNOHANG, nil)
			if err == nil && pid == cmd.Process.Pid {
				// FFmpeg exited with error (e.g. subtitle stream missing/invalid)
				log.Printf("[Transcoder] FFmpeg exited with subtitles enabled (status: %v). Retrying without subtitles...", ws)
				cancel()
				cfg.SubtitleTrack = -1
				return d.StartPipeline(cfg)
			}
		}
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
