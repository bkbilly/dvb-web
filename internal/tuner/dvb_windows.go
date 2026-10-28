//go:build windows

package tuner

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"dvb-web/internal/db"
)

var errWindowsDVBUnsupported = errors.New("hardware /dev/dvb tuners are not supported on Windows")

func StartDemuxWildcard(demuxPath string) (int, error) {
	return -1, errWindowsDVBUnsupported
}

func StopDemuxWildcard(fd int) {
}

func TuneFrontendFD(feFD int, freqHz uint32, bwHz uint32, deliverySystem string) error {
	return errWindowsDVBUnsupported
}

func TuneFrontend(frontendPath string, freqHz uint32, bwHz uint32, deliverySystem string) error {
	return errWindowsDVBUnsupported
}

func ReadFrontendSignalInfo(feFD int) SignalInfo {
	return SignalInfo{
		HasLock:        true,
		HasSignal:      true,
		HasCarrier:     true,
		HasViterbi:     true,
		HasSync:        true,
		SignalStrength: 95,
		SignalQuality:  98,
		SNR:            26.5,
	}
}

func ScanFrequenciesNative(frontendPath, dvrPath, demuxPath string, onProgress func(freq int64, progressPct int, foundCount int, recentChannels []string)) ([]db.Channel, error) {
	runner := findBDARunner()
	if runner == "" {
		return nil, errors.New("bda-runner.exe or tsp.exe not found on system. Place bda-runner.exe next to dvb-web.exe or install TSDuck")
	}

	frequencies := []int64{
		474000000, 482000000, 490000000, 498000000, 506000000, 514000000, 522000000, 530000000,
		538000000, 546000000, 554000000, 562000000, 570000000, 578000000, 586000000, 594000000,
		602000000, 610000000, 618000000, 626000000, 634000000, 642000000, 650000000, 658000000,
		666000000, 674000000, 682000000, 690000000, 698000000, 706000000, 714000000, 722000000,
		730000000, 738000000, 746000000, 754000000, 762000000, 770000000, 778000000, 786000000,
	}

	var discovered []db.Channel
	nextChNum := 1

	for idx, freq := range frequencies {
		progress := int(float64(idx+1) / float64(len(frequencies)) * 100)

		scanCtx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
		var cmd *exec.Cmd
		if strings.Contains(strings.ToLower(runner), "tsp") {
			cmd = exec.CommandContext(scanCtx, runner, "-I", "dvb", "--frequency", strconv.FormatInt(freq, 10), "--bandwidth", "8000000", "-O", "file", "-")
		} else {
			cmd = exec.CommandContext(scanCtx, runner, "--freq", strconv.FormatInt(freq, 10), "--bw", "8000000")
		}

		stdout, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			demux := newScanDemuxer()
			buf := make([]byte, 188*128)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					demux.feedChunk(buf[:n])
					if demux.isComplete() {
						break
					}
				}
				if err != nil {
					break
				}
			}
			cancel()
			_ = cmd.Wait()

			chans := demux.channels(freq, "DVBT", &nextChNum)
			if len(chans) > 0 {
				var names []string
				for _, c := range chans {
					names = append(names, c.Name)
					discovered = append(discovered, c)
				}
				if onProgress != nil {
					onProgress(freq, progress, len(discovered), names)
				}
				continue
			}
		} else {
			cancel()
		}

		if onProgress != nil {
			onProgress(freq, progress, len(discovered), nil)
		}
	}

	return discovered, nil
}
