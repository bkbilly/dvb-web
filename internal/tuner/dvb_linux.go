package tuner

import (
	"encoding/binary"
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"

	"dvb-web/internal/db"
	"dvb-web/internal/epg"
)

// Linux DVB API ioctl numbers
const (
	ioctlFEGetInfo            = 0x80a86f3d
	ioctlFEReadStatus         = 0x80046f45
	ioctlFEReadBER            = 0x80046f46
	ioctlFEReadSignalStrength = 0x80026f47
	ioctlFEReadSNR            = 0x80026f48
	ioctlFESetProperty        = 0x40106f52

	ioctlDMXStart        = 0x00006f29
	ioctlDMXStop         = 0x00006f2a
	ioctlDMXSetPesFilter = 0x40146f2c
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

	p := dmxPesFilterParams{
		pid:     0x2000, // Wildcard pass all PIDs
		input:   0,      // DMX_IN_FRONTEND
		output:  2,      // DMX_OUT_TS_TAP
		pesType: 16,     // DMX_PES_OTHER
		flags:   0,
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlDMXSetPesFilter, uintptr(unsafe.Pointer(&p)))
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

	// Wait up to 1.5s for signal lock
	timeout := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(timeout) {
		time.Sleep(100 * time.Millisecond)
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

type SignalInfo struct {
	HasLock        bool    `json:"has_lock"`
	HasSignal      bool    `json:"has_signal"`
	SignalStrength int     `json:"signal_strength"` // 0-100%
	SignalQuality  int     `json:"signal_quality"`  // 0-100%
	SNR            float64 `json:"snr_db"`          // SNR in dB
	BER            uint32  `json:"ber"`
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
		HasLock:   (status & feHasLock) != 0,
		HasSignal: (status & feHasSignal) != 0,
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

	return info
}

// ScanFrequenciesNative sweeps UHF & VHF TV channels to discover active broadcasts
func ScanFrequenciesNative(frontendPath, dvrPath, demuxPath string, onProgress func(freq int64, progressPct int, foundCount int)) ([]db.Channel, error) {
	feFD, err := syscall.Open(frontendPath, syscall.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", frontendPath, err)
	}
	defer syscall.Close(feFD)

	// Keep demuxer running with wildcard TS filter during scan
	dmxFD, err := StartDemuxWildcard(demuxPath)
	if err != nil {
		log.Printf("[DVB Scanner] Warning: failed to start demux wildcard: %v", err)
	} else {
		defer StopDemuxWildcard(dmxFD)
	}

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
			onProgress(int64(freq), progressPct, len(discovered))
		}

		locked, delivSys := tuneAndCheck(feFD, freq, 8000000)
		if locked {
			log.Printf("[DVB Scanner] *** Signal locked at %.2f MHz (%s)! Reading multiplex...", float64(freq)/1e6, delivSys)
			channels := readChannelsFromTS(dvrPath, int64(freq), delivSys, &channelNum)
			if len(channels) > 0 {
				log.Printf("[DVB Scanner] Found %d channels on %.2f MHz", len(channels), float64(freq)/1e6)
				discovered = append(discovered, channels...)
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

	// Poll signal status
	for i := 0; i < 5; i++ {
		time.Sleep(80 * time.Millisecond)
		var status uint32
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadStatus, uintptr(unsafe.Pointer(&status)))
		if errno == 0 {
			if (status & feHasLock) != 0 {
				return true
			}
			if (status & (feHasCarrier | feHasSync | feHasSignal)) != 0 {
				time.Sleep(200 * time.Millisecond)
				syscall.Syscall(syscall.SYS_IOCTL, uintptr(feFD), ioctlFEReadStatus, uintptr(unsafe.Pointer(&status)))
				if (status & feHasLock) != 0 {
					return true
				}
			}
		}
	}
	return false
}

// readChannelsFromTS reads MPEG-TS from dvr0 to extract SDT (Service Names) & PAT (Service IDs)
func readChannelsFromTS(dvrPath string, freq int64, deliverySys string, nextNum *int) []db.Channel {
	fd, err := syscall.Open(dvrPath, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer syscall.Close(fd)

	serviceNames := make(map[int]string)
	servicePIDs := make(map[int]struct{ vPID, aPID int })

	buf := make([]byte, 188*50)
	deadline := time.Now().Add(2500 * time.Millisecond)

	for time.Now().Before(deadline) {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			break
		}
		if n < 188 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		for i := 0; i+188 <= n; i += 188 {
			pkt := buf[i : i+188]
			if pkt[0] != 0x47 {
				continue
			}

			pid := (int(pkt[1]&0x1F) << 8) | int(pkt[2])
			pusi := (pkt[1] & 0x40) != 0

			// SDT (Service Description Table) has PID 0x0011 (17)
			if pid == 0x0011 && pusi {
				offset := 4
				adapt := (pkt[3] >> 4) & 0x03
				if adapt == 2 {
					continue // adaptation field only
				}
				if adapt == 3 {
					offset += 1 + int(pkt[4])
				}
				if offset >= 188 {
					continue
				}

				ptr := int(pkt[offset])
				secOffset := offset + 1 + ptr
				if secOffset < 188 {
					parseSDT(pkt[secOffset:], serviceNames)
				}
			}
		}

		if len(serviceNames) > 0 {
			time.Sleep(150 * time.Millisecond)
		}
	}

	var results []db.Channel
	for sid, name := range serviceNames {
		pids := servicePIDs[sid]
		results = append(results, db.Channel{
			Name:           name,
			Number:         *nextNum,
			Frequency:      freq,
			Bandwidth:      8000000,
			DeliverySystem: deliverySys,
			ServiceID:      sid,
			VideoPID:       pids.vPID,
			AudioPID:       pids.aPID,
			Enabled:        true,
		})
		*nextNum++
	}

	return results
}

// parseSDT parses Service Names from DVB SDT section
func parseSDT(sec []byte, serviceNames map[int]string) {
	if len(sec) < 11 {
		return
	}
	tableID := sec[0]
	if tableID != 0x42 && tableID != 0x46 {
		return
	}

	sectionLength := int(binary.BigEndian.Uint16(sec[1:3]) & 0x0FFF)
	if len(sec) < sectionLength+3 {
		return
	}

	offset := 11
	endOffset := sectionLength + 3 - 4 // minus CRC

	for offset+5 <= endOffset {
		serviceID := int(binary.BigEndian.Uint16(sec[offset : offset+2]))
		descriptorsLoopLength := int(binary.BigEndian.Uint16(sec[offset+3:offset+5]) & 0x0FFF)
		offset += 5

		descEnd := offset + descriptorsLoopLength
		if descEnd > endOffset {
			break
		}

		for offset+2 <= descEnd {
			tag := sec[offset]
			length := int(sec[offset+1])
			offset += 2

			if offset+length > descEnd {
				break
			}

			// Service Descriptor Tag 0x48
			if tag == 0x48 && length >= 3 {
				data := sec[offset : offset+length]
				providerLen := int(data[1])
				nameOffset := 2 + providerLen
				if nameOffset < len(data) {
					nameLen := int(data[nameOffset])
					if nameOffset+1+nameLen <= len(data) {
						serviceName := epg.DecodeDVBString(data[nameOffset+1 : nameOffset+1+nameLen])
						if serviceName != "" {
							serviceNames[serviceID] = serviceName
						}
					}
				}
			}

			offset += length
		}
	}
}
