//go:build windows

package tuner

import (
	"dvb-web/internal/db"
	"errors"
)

// Windows DVB stubs - hardware /dev/dvb tuners are only present on Linux.
// On Windows, the system operates in virtual / simulated tuner mode or via network sources.

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
		SignalStrength: 100,
		SignalQuality:  100,
		SNR:            30.0,
	}
}

func ScanFrequenciesNative(frontendPath, dvrPath, demuxPath string, onProgress func(freq int64, progressPct int, foundCount int, recentChannels []string)) ([]db.Channel, error) {
	return nil, errWindowsDVBUnsupported
}
