//go:build !windows

package tuner

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"dvb-web/internal/epg"

	"golang.org/x/sys/unix"
)

func discoverPlatformTuners(basePort int, ep *epg.Parser) []*Tuner {
	matches, _ := filepath.Glob("/dev/dvb/adapter*")
	re := regexp.MustCompile(`/dev/dvb/adapter(\d+)`)

	var tuners []*Tuner
	portCounter := basePort
	for _, m := range matches {
		sub := re.FindStringSubmatch(m)
		if len(sub) == 2 {
			id, _ := strconv.Atoi(sub[1])
			t := &Tuner{
				ID:             id,
				Name:           fmt.Sprintf("DVB Adapter %d", id),
				FrontendPath:   filepath.Join(m, "frontend0"),
				DVRPath:        filepath.Join(m, "dvr0"),
				DemuxPath:      filepath.Join(m, "demux0"),
				status:         "idle",
				viewers:        make(map[string]*ViewerSession),
				channels:       make(map[string]*ChannelStream),
				detectedTracks: make(map[int]ChannelTracks),
				pmtPIDs:        make(map[int]int),
				videoUDPPort:   portCounter,
				audioUDPPort:   portCounter + 2,
				epgParser:      ep,
				Enabled:        true,
			}
			portCounter += 4
			tuners = append(tuners, t)
			log.Printf("[TunerPool] Discovered hardware tuner: %s (%s)", t.Name, m)
		}
	}
	return tuners
}

func openFrontendDevice(path string) (int, error) {
	return syscall.Open(path, syscall.O_RDWR, 0)
}

func openDVRDevice(path string) (int, error) {
	return syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func closeDeviceFD(fd int) error {
	if fd > 0 {
		return syscall.Close(fd)
	}
	return nil
}

func drainDVRDevice(fd int, buf []byte) (int, error) {
	return syscall.Read(fd, buf)
}

// pollAndReadDVR waits up to 50ms for data ready on dvrFD and reads into buf
func pollAndReadDVR(ctx context.Context, dvrFD int, buf []byte) (int, error) {
	pfd := []unix.PollFd{{Fd: int32(dvrFD), Events: unix.POLLIN}}
	nReady, err := unix.Poll(pfd, 50)
	if err != nil {
		if err == unix.EINTR {
			return 0, nil
		}
		return 0, err
	}
	if nReady == 0 {
		return 0, nil
	}

	n, err := syscall.Read(dvrFD, buf)
	if err != nil {
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK || err == syscall.EINTR {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

func startBDATSPump(ctx context.Context, t *Tuner, freqHz int64, bwHz int, tsQueue chan<- []byte, parseQueue chan<- []byte) error {
	return fmt.Errorf("BDA is not supported on Linux")
}
