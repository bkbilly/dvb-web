package tuner

import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"

	"dvb-web/internal/db"
)

// Linux DVB API ioctl numbers
const (
	ioctlFEGetInfo            = 0x80a86f3d
	ioctlFEReadStatus         = 0x80046f45
	ioctlFEReadBER            = 0x80046f46
	ioctlFEReadSignalStrength   = 0x80026f47
	ioctlFEReadSNR              = 0x80026f48
	ioctlFEReadUncorrectedBlocks = 0x80046f49
	ioctlFESetProperty          = 0x40106f52

	ioctlDMXStart         = 0x00006f29
	ioctlDMXStop          = 0x00006f2a
	ioctlDMXSetPesFilter  = 0x40146f2c
	ioctlDMXSetBufferSize = 0x00006f2d
)

// DTV command codes
const (
	dtvTune             = 1
	dtvClear            = 2
	dtvFrequency        = 3
	dtvModulation       = 4
	dtvBandwidthHz      = 5
	dtvInversion        = 6
	dtvDeliverySystem   = 17
	dtvCodeRateHP       = 36
	dtvCodeRateLP       = 37
	dtvGuardInterval    = 38
	dtvTransmissionMode = 39
	dtvHierarchy        = 40

	sysDVBT  = 3
	sysDVBT2 = 16

	inversionAuto        = 2
	fecAuto              = 9
	qamAuto              = 6
	transmissionModeAuto = 2
	guardIntervalAuto    = 4
	hierarchyAuto        = 4

	feHasSignal  = 0x01
	feHasCarrier = 0x02
	feHasViterbi = 0x04
	feHasSync    = 0x08
	feHasLock    = 0x10
)

// Exact Linux kernel packed struct layouts (sizeof dtvProperty = 76, sizeof dtvProperties = 16)
type dtvProperty struct {
	cmd      uint32
	reserved [3]uint32
	val      uint32
	res      [13]uint32
	result   int32
}

type dtvProperties struct {
	num   uint32
	_pad  uint32
	props uintptr
}

// struct dmx_pes_filter_params (sizeof = 20)
type dmxPesFilterParams struct {
	pid     uint16
	_pad    uint16
	input   uint32
	output  uint32
	pesType uint32
	flags   uint32
}

// StartDemuxWildcard opens demuxPath and routes all TS packets to DVR device
func StartDemuxWildcard(demuxPath string) (int, error) {
	fd, err := syscall.Open(demuxPath, syscall.O_RDWR, 0)
	if err != nil {
		return -1, fmt.Errorf("failed to open demux %s: %w", demuxPath, err)
	}

	// Expand kernel demux ring buffer to 8MB BEFORE setting the filter.
	// In the Linux DVB driver (dmxdev.c), DMX_SET_BUFFER_SIZE returns -EBUSY if called after DMX_SET_PES_FILTER.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlDMXSetBufferSize, uintptr(8*1024*1024))
	if errno != 0 {
		log.Printf("[DVB] Warning: DMX_SET_BUFFER_SIZE (8MB) failed: %v", errno)
	}

	p := dmxPesFilterParams{
		pid:     0x2000, // Wildcard pass all PIDs
		input:   0,      // DMX_IN_FRONTEND
		output:  2,      // DMX_OUT_TS_TAP
		pesType: 16,     // DMX_PES_OTHER
		flags:   0,
	}

	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlDMXSetPesFilter, uintptr(unsafe.Pointer(&p)))
	if errno != 0 {
		syscall.Close(fd)
		return -1, fmt.Errorf("DMX_SET_PES_FILTER failed: %v", errno)
	}

	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlDMXStart, 0)
	if errno != 0 {
		syscall.Close(fd)
		return -1, fmt.Errorf("DMX_START failed: %v", errno)
	}

	return fd, nil
}

// StopDemuxWildcard cleanly stops demux filtering and closes the file descriptor
func StopDemuxWildcard(fd int) {
	if fd > 0 {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlDMXStop, 0)
		_ = syscall.Close(fd)
	}
}

// TuneFrontendFD tunes an already open frontend file descriptor
func TuneFrontendFD(feFD int, freqHz uint32, bwHz uint32, deliverySystem string) error {
	sys := uint32(sysDVBT)
	if deliverySystem == "DVBT2" {
		sys = sysDVBT2
	}

	props := []dtvProperty{
		{cmd: dtvClear},
		{cmd: dtvDeliverySystem, val: sys},
		{cmd: dtvFrequency, val: freqHz},
		{cmd: dtvBandwidthHz, val: bwHz},
		{cmd: dtvInversion, val: inversionAuto},
		{cmd: dtvCodeRateHP, val: fecAuto},
		{cmd: dtvCodeRateLP, val: fecAuto},
		{cmd: dtvModulation, val: qamAuto},
		{cmd: dtvTransmissionMode, val: transmissionModeAuto},
		{cmd: dtvGuardInterval, val: guardIntervalAuto},
		{cmd: dtvHierarchy, val: hierarchyAuto},
		{cmd: dtvTune},
	}

	cmdseq := dtvProperties{
		num:   uint32(len(props)),
		props: uintptr(unsafe.Pointer(&props[0])),
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFESetProperty, uintptr(unsafe.Pointer(&cmdseq)))
	if errno != 0 {
		return fmt.Errorf("FE_SET_PROPERTY ioctl failed: %v", errno)
	}

	// Wait up to 1.5s for signal lock (poll every 25ms for instantaneous lock acquisition)
	timeout := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(timeout) {
		time.Sleep(25 * time.Millisecond)
		var status uint32
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadStatus, uintptr(unsafe.Pointer(&status)))
		if errno == 0 && (status&feHasLock) != 0 {
			log.Printf("[DVB] Tuner locked on %.2f MHz (status=0x%02x)", float64(freqHz)/1e6, status)
			return nil
		}
	}

	log.Printf("[DVB] Tuner tuned to %.2f MHz (waiting for lock...)", float64(freqHz)/1e6)
	return nil
}

// TuneFrontend tunes the physical frontend device to the given frequency and bandwidth with full AUTO parameters
func TuneFrontend(frontendPath string, freqHz uint32, bwHz uint32, deliverySystem string) error {
	fd, err := syscall.Open(frontendPath, syscall.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open frontend device %s: %w", frontendPath, err)
	}
	defer syscall.Close(fd)
	return TuneFrontendFD(fd, freqHz, bwHz, deliverySystem)
}

// ReadFrontendSignalInfo queries hardware signal metrics from an open frontend FD
func ReadFrontendSignalInfo(feFD int) SignalInfo {
	if feFD <= 0 {
		return SignalInfo{}
	}

	var status uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadStatus, uintptr(unsafe.Pointer(&status)))
	if errno != 0 {
		return SignalInfo{}
	}

	info := SignalInfo{
		HasLock:    (status & feHasLock) != 0,
		HasSignal:  (status & feHasSignal) != 0,
		HasCarrier: (status & feHasCarrier) != 0,
		HasViterbi: (status & feHasViterbi) != 0,
		HasSync:    (status & feHasSync) != 0,
	}

	// Read Signal Strength (0..65535)
	var rawStrength uint16
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadSignalStrength, uintptr(unsafe.Pointer(&rawStrength)))
	if errno == 0 {
		info.SignalStrength = int((uint32(rawStrength) * 100) / 65535)
	}

	// Read SNR / Quality
	var rawSNR uint16
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadSNR, uintptr(unsafe.Pointer(&rawSNR)))
	if errno == 0 {
		if rawSNR > 655 {
			info.SignalQuality = int((uint32(rawSNR) * 100) / 65535)
			info.SNR = float64(rawSNR) / 655.35
		} else {
			// Tenths of dB (e.g. 182 = 18.2 dB, typical on RTL2832 and DVB-T)
			info.SNR = float64(rawSNR) / 10.0
			q := int(((info.SNR - 10.0) / 15.0) * 100.0)
			if q < 0 {
				q = 0
			}
			if q > 100 {
				q = 100
			}
			info.SignalQuality = q
		}
	}

	if info.HasLock && info.SignalQuality == 0 {
		if info.SignalStrength > 0 {
			info.SignalQuality = info.SignalStrength
		} else {
			info.SignalQuality = 90
		}
	}

	// Read BER
	var rawBER uint32
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadBER, uintptr(unsafe.Pointer(&rawBER)))
	if errno == 0 {
		info.BER = rawBER
	}

	// Read Uncorrected Blocks (UNC)
	var rawUNC uint32
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadUncorrectedBlocks, uintptr(unsafe.Pointer(&rawUNC)))
	if errno == 0 {
		info.UNC = rawUNC
	}

	return info
}

// ScanFrequenciesNative sweeps UHF & VHF TV channels to discover active broadcasts
func ScanFrequenciesNative(frontendPath, dvrPath, demuxPath string, onProgress func(freq int64, progressPct int, foundCount int, recentChannels []string)) ([]db.Channel, error) {
	feFD, err := syscall.Open(frontendPath, syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", frontendPath, err)
	}
	defer syscall.Close(feFD)

	// Keep wildcard demux filter open across the scan so USB/DMA streaming remains warm and stable
	dmxFD, err := StartDemuxWildcard(demuxPath)
	if err != nil {
		log.Printf("[DVB Scanner] Warning: failed to start demux wildcard: %v", err)
	} else {
		defer StopDemuxWildcard(dmxFD)
	}

	dvrFD, err := syscall.Open(dvrPath, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", dvrPath, err)
	}
	defer syscall.Close(dvrFD)

	// Standard UHF DVB-T/T2 channels 21 to 60 (474 MHz to 786 MHz in 8 MHz steps)
	var frequencies []uint32
	for f := uint32(474000000); f <= 786000000; f += 8000000 {
		frequencies = append(frequencies, f)
	}
	// Common VHF Band III channels (177.5 MHz - 226.5 MHz in 7 MHz steps)
	for f := uint32(177500000); f <= 226500000; f += 7000000 {
		frequencies = append(frequencies, f)
	}

	log.Printf("[DVB Scanner] Starting frequency sweep across %d frequencies...", len(frequencies))

	var discovered []db.Channel
	channelNum := 1

	for idx, freq := range frequencies {
		progressPct := int(float64(idx+1) / float64(len(frequencies)) * 100)
		if onProgress != nil {
			onProgress(int64(freq), progressPct, len(discovered), nil)
		}

		bw := uint32(8000000)
		if freq < 300000000 {
			bw = 7000000
		}

		locked, delivSys := tuneAndCheck(feFD, freq, bw)
		if locked {
			log.Printf("[DVB Scanner] *** Signal locked at %.2f MHz (%s)! Reading multiplex...", float64(freq)/1e6, delivSys)
			channels := readChannelsFromTS(dvrFD, int64(freq), delivSys, &channelNum)
			if len(channels) > 0 {
				log.Printf("[DVB Scanner] Found %d channels on %.2f MHz", len(channels), float64(freq)/1e6)
				discovered = append(discovered, channels...)
				if onProgress != nil {
					var recent []string
					for _, ch := range channels {
						recent = append(recent, ch.Name)
					}
					onProgress(int64(freq), progressPct, len(discovered), recent)
				}
			}
		}
	}

	log.Printf("[DVB Scanner] Scan finished. Total channels discovered: %d", len(discovered))
	return discovered, nil
}

func tuneAndCheck(feFD int, freq uint32, bw uint32) (bool, string) {
	if tryTune(feFD, freq, bw, sysDVBT) {
		return true, "DVBT"
	}
	if tryTune(feFD, freq, bw, sysDVBT2) {
		return true, "DVBT2"
	}
	return false, ""
}

func tryTune(feFD int, freq uint32, bw uint32, sys uint32) bool {
	props := []dtvProperty{
		{cmd: dtvClear},
		{cmd: dtvDeliverySystem, val: sys},
		{cmd: dtvFrequency, val: freq},
		{cmd: dtvBandwidthHz, val: bw},
		{cmd: dtvInversion, val: inversionAuto},
		{cmd: dtvCodeRateHP, val: fecAuto},
		{cmd: dtvCodeRateLP, val: fecAuto},
		{cmd: dtvModulation, val: qamAuto},
		{cmd: dtvTransmissionMode, val: transmissionModeAuto},
		{cmd: dtvGuardInterval, val: guardIntervalAuto},
		{cmd: dtvHierarchy, val: hierarchyAuto},
		{cmd: dtvTune},
	}
	cmdseq := dtvProperties{
		num:   uint32(len(props)),
		props: uintptr(unsafe.Pointer(&props[0])),
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFESetProperty, uintptr(unsafe.Pointer(&cmdseq)))
	if errno != 0 {
		return false
	}

	// Poll signal status up to 800ms
	// Demodulators take 400-750ms to acquire lock
	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
		var status uint32
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadStatus, uintptr(unsafe.Pointer(&status)))
		if errno == 0 && (status&feHasLock) != 0 {
			return true
		}
	}
	return false
}


// readChannelsFromTS reads MPEG-TS from dvr0 to extract SDT (Names), PAT/PMT (PIDs) & AIT (HbbTV URLs)
func readChannelsFromTS(dvrFD int, freq int64, deliverySys string, nextNum *int) []db.Channel {
	// Drain any residual stale packets from kernel buffer before scanning this frequency
	drainBuf := make([]byte, 64*1024)
	for {
		n, _ := syscall.Read(dvrFD, drainBuf)
		if n <= 0 {
			break
		}
	}

	demuxer := newScanDemuxer()
	buf := make([]byte, 32*1024)
	deadline := time.Now().Add(3000 * time.Millisecond)
	startTime := time.Now()

	for time.Now().Before(deadline) {
		n, err := syscall.Read(dvrFD, buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			break
		}
		if n <= 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		demuxer.feedChunk(buf[:n])

		if time.Since(startTime) > 1000*time.Millisecond && demuxer.isComplete() {
			break
		}
	}

	return demuxer.channels(freq, deliverySys, nextNum)
}

// parseSDT parses Service Names and Types from DVB SDT section
