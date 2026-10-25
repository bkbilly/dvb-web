package tuner

import (
	"encoding/binary"
	"fmt"
	"log"
	"sort"
	"strings"
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

type SignalInfo struct {
	HasLock        bool    `json:"has_lock"`
	HasSignal      bool    `json:"has_signal"`
	HasCarrier     bool    `json:"has_carrier"`
	HasViterbi     bool    `json:"has_viterbi"`
	HasSync        bool    `json:"has_sync"`
	SignalStrength int     `json:"signal_strength"` // 0-100%
	SignalQuality  int     `json:"signal_quality"`  // 0-100%
	SNR            float64 `json:"snr_db"`          // SNR in dB
	BER            uint32  `json:"ber"`
	UNC            uint32  `json:"unc"`
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

type scanDemuxer struct {
	patServices     map[int]int
	pmtPIDs         map[int]int
	aitPIDs         map[int]int
	serviceNames    map[int]string
	serviceTypes    map[int]int
	servicePIDs     map[int]struct{ vPID, aPID int }
	serviceHbbTV    map[int]string
	serviceTeletext map[int]bool

	pendingSec  map[int][]byte
	expectedLen map[int]int

	leftover []byte
	synced   bool
}

func newScanDemuxer() *scanDemuxer {
	return &scanDemuxer{
		patServices:     make(map[int]int),
		pmtPIDs:         make(map[int]int),
		aitPIDs:         make(map[int]int),
		serviceNames:    make(map[int]string),
		serviceTypes:    make(map[int]int),
		servicePIDs:     make(map[int]struct{ vPID, aPID int }),
		serviceHbbTV:    make(map[int]string),
		serviceTeletext: make(map[int]bool),
		pendingSec:      make(map[int][]byte),
		expectedLen:     make(map[int]int),
	}
}

func (d *scanDemuxer) feedChunk(chunk []byte) {
	data := chunk
	if len(d.leftover) > 0 {
		data = append(d.leftover, data...)
		d.leftover = nil
	}

	// Synchronize to 188-byte MPEG-TS packet boundary starting with 0x47
	if !d.synced {
		syncIdx := -1
		for i := 0; i+188 < len(data); i++ {
			if data[i] == 0x47 && data[i+188] == 0x47 {
				if i+188*2 < len(data) && data[i+188*2] != 0x47 {
					continue
				}
				syncIdx = i
				break
			}
		}
		if syncIdx < 0 {
			if len(data) > 188 {
				d.leftover = append(d.leftover, data[len(data)-188:]...)
			} else {
				d.leftover = append(d.leftover, data...)
			}
			return
		}
		data = data[syncIdx:]
		d.synced = true
	}

	if len(data) > 0 && data[0] != 0x47 {
		d.synced = false
		d.leftover = append(d.leftover, data...)
		if len(d.leftover) > 64*1024 {
			d.leftover = d.leftover[len(d.leftover)-188:]
		}
		return
	}

	// Preserve trailing incomplete TS packet for the next read
	rem := len(data) % 188
	if rem > 0 {
		d.leftover = append(d.leftover, data[len(data)-rem:]...)
		data = data[:len(data)-rem]
	}

	for i := 0; i+188 <= len(data); i += 188 {
		pkt := data[i : i+188]
		if pkt[0] != 0x47 {
			d.synced = false
			break
		}

		// Skip packets with Transport Error Indicator (TEI) set
		if (pkt[1] & 0x80) != 0 {
			continue
		}

		pid := (int(pkt[1]&0x1F) << 8) | int(pkt[2])
		pusi := (pkt[1] & 0x40) != 0

		_, isPMT := d.pmtPIDs[pid]
		_, isAIT := d.aitPIDs[pid]
		if pid != 0x0000 && pid != 0x0011 && !isPMT && !isAIT {
			continue
		}

		adapt := (pkt[3] >> 4) & 0x03
		if adapt == 2 {
			continue // adaptation field only
		}
		offset := 4
		if adapt == 3 {
			adaptLen := int(pkt[4])
			offset += 1 + adaptLen
		}
		if offset >= 188 {
			continue
		}

		payload := pkt[offset:]
		if len(payload) == 0 {
			continue
		}

		var sectionsToProcess [][]byte

		if pusi {
			pointerField := int(payload[0])
			if len(d.pendingSec[pid]) > 0 {
				if pointerField > 0 {
					avail := pointerField
					if avail > len(payload)-1 {
						avail = len(payload) - 1
					}
					d.pendingSec[pid] = append(d.pendingSec[pid], payload[1:1+avail]...)
				}
				if len(d.pendingSec[pid]) >= d.expectedLen[pid] {
					sectionsToProcess = append(sectionsToProcess, d.pendingSec[pid][:d.expectedLen[pid]])
				}
			}
			d.pendingSec[pid] = nil
			d.expectedLen[pid] = 0

			secStart := 1 + pointerField
			if secStart < len(payload) {
				curr := payload[secStart:]
				for len(curr) >= 3 {
					if curr[0] == 0xFF {
						break // stuffing
					}
					secLen := (int(curr[1]&0x0F) << 8) | int(curr[2])
					totalLen := secLen + 3
					if secLen < 1 || totalLen > 4096 {
						break
					}
					if len(curr) >= totalLen {
						sectionsToProcess = append(sectionsToProcess, curr[:totalLen])
						curr = curr[totalLen:]
					} else {
						d.pendingSec[pid] = append([]byte(nil), curr...)
						d.expectedLen[pid] = totalLen
						break
					}
				}
			}
		} else {
			if len(d.pendingSec[pid]) > 0 {
				d.pendingSec[pid] = append(d.pendingSec[pid], payload...)
				if len(d.pendingSec[pid]) >= d.expectedLen[pid] {
					sectionsToProcess = append(sectionsToProcess, d.pendingSec[pid][:d.expectedLen[pid]])
					d.pendingSec[pid] = nil
					d.expectedLen[pid] = 0
				}
			}
		}

		for _, sec := range sectionsToProcess {
			d.processSection(pid, sec)
		}
	}
}

func (d *scanDemuxer) processSection(pid int, sec []byte) {
	if len(sec) < 3 {
		return
	}
	tableID := sec[0]

	// 1. PAT (PID 0x0000, Table ID 0x00)
	if pid == 0x0000 && tableID == 0x00 {
		secLen := (int(sec[1]&0x0F) << 8) | int(sec[2])
		end := 3 + secLen - 4 // minus CRC
		if end > len(sec) {
			end = len(sec)
		}
		for p := 8; p+4 <= end; p += 4 {
			sid := (int(sec[p]) << 8) | int(sec[p+1])
			pmtPID := (int(sec[p+2]&0x1F) << 8) | int(sec[p+3])
			if sid != 0 {
				d.patServices[sid] = pmtPID
				d.pmtPIDs[pmtPID] = sid
			}
		}
		return
	}

	// 2. SDT: Actual transport stream ONLY (PID 0x0011, Table ID 0x42)
	// ETSI EN 300 468: 0x42 is actual TS; 0x46 is other TS.
	// SDT other (0x46) MUST be ignored so channels from other multiplexes are not wrongly assigned.
	if pid == 0x0011 && tableID == 0x42 {
		parseSDT(sec, d.serviceNames, d.serviceTypes)
		return
	}

	// 3. PMT (PID in pmtPIDs, Table ID 0x02)
	if sid, isPMT := d.pmtPIDs[pid]; isPMT && tableID == 0x02 && len(sec) >= 12 {
		progInfoLen := (int(sec[10]&0x0F) << 8) | int(sec[11])
		esOffset := 12 + progInfoLen
		end := len(sec) - 4
		if end > len(sec) {
			end = len(sec)
		}

		pids := d.servicePIDs[sid]
		for p := esOffset; p+5 <= end; {
			stype := sec[p]
			elemPID := (int(sec[p+1]&0x1F) << 8) | int(sec[p+2])
			esInfoLen := (int(sec[p+3]&0x0F) << 8) | int(sec[p+4])
			descEnd := p + 5 + esInfoLen
			if descEnd > end {
				descEnd = end
			}

			// Video Stream Types (MPEG1/2, H.264, HEVC, AVS)
			if pids.vPID == 0 && (stype == 0x01 || stype == 0x02 || stype == 0x1B || stype == 0x24 || stype == 0x42) {
				pids.vPID = elemPID
			}
			// Audio Stream Types (MPEG audio, AAC ADTS, AAC LATM, AC-3)
			if pids.aPID == 0 && (stype == 0x03 || stype == 0x04 || stype == 0x0F || stype == 0x11 || stype == 0x81) {
				pids.aPID = elemPID
			}
			if stype == 0x05 || stype == 0x0B {
				d.aitPIDs[elemPID] = sid
			}

			for dPos := p + 5; dPos+2 <= descEnd; {
				dtag := sec[dPos]
				dlen := int(sec[dPos+1])
				if dtag == 0x6F { // application_signalling_descriptor
					d.aitPIDs[elemPID] = sid
				}
				if dtag == 0x56 { // teletext_descriptor
					d.serviceTeletext[sid] = true
				}
				if pids.aPID == 0 && (dtag == 0x6A || dtag == 0x7A || dtag == 0x7B) {
					pids.aPID = elemPID
				}
				dPos += 2 + dlen
			}
			p = descEnd
		}
		d.servicePIDs[sid] = pids
		return
	}

	// 4. AIT (Table ID 0x74)
	if tableID == 0x74 {
		url := ParseAITSection(sec)
		if url != "" {
			sid := d.aitPIDs[pid]
			if sid != 0 {
				d.serviceHbbTV[sid] = url
			} else {
				for s, name := range d.serviceNames {
					if d.serviceHbbTV[s] == "" && strings.Contains(strings.ToUpper(string(sec)), strings.ToUpper(name)) {
						d.serviceHbbTV[s] = url
						break
					}
				}
			}
		}
	}
}

func (d *scanDemuxer) isComplete() bool {
	if len(d.patServices) == 0 {
		return false
	}
	for sid := range d.patServices {
		if _, ok := d.serviceNames[sid]; !ok {
			return false
		}
	}
	return true
}

func (d *scanDemuxer) channels(freq int64, deliverySys string, nextNum *int) []db.Channel {
	bw := 8000000
	if freq < 300000000 {
		bw = 7000000
	}

	var results []db.Channel
	if len(d.patServices) > 0 {
		var sids []int
		for sid := range d.patServices {
			sids = append(sids, sid)
		}
		sort.Ints(sids)

		for _, sid := range sids {
			name := d.serviceNames[sid]
			if name == "" {
				name = fmt.Sprintf("Service %d", sid)
			}
			pids := d.servicePIDs[sid]
			sType := d.serviceTypes[sid]
			isRadio := (sType == 0x02 || sType == 0x07 || sType == 0x0A)
			results = append(results, db.Channel{
				Name:           name,
				Number:         *nextNum,
				Frequency:      freq,
				Bandwidth:      bw,
				DeliverySystem: deliverySys,
				ServiceID:      sid,
				VideoPID:       pids.vPID,
				AudioPID:       pids.aPID,
				Enabled:        true,
				IsRadio:        isRadio,
				HasTeletext:    d.serviceTeletext[sid],
				HbbtvURL:       d.serviceHbbTV[sid],
			})
			*nextNum++
		}
	} else if len(d.serviceNames) > 0 {
		var sids []int
		for sid := range d.serviceNames {
			sids = append(sids, sid)
		}
		sort.Ints(sids)

		for _, sid := range sids {
			name := d.serviceNames[sid]
			pids := d.servicePIDs[sid]
			sType := d.serviceTypes[sid]
			isRadio := (sType == 0x02 || sType == 0x07 || sType == 0x0A)
			results = append(results, db.Channel{
				Name:           name,
				Number:         *nextNum,
				Frequency:      freq,
				Bandwidth:      bw,
				DeliverySystem: deliverySys,
				ServiceID:      sid,
				VideoPID:       pids.vPID,
				AudioPID:       pids.aPID,
				Enabled:        true,
				IsRadio:        isRadio,
				HasTeletext:    d.serviceTeletext[sid],
				HbbtvURL:       d.serviceHbbTV[sid],
			})
			*nextNum++
		}
	}

	return results
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
func parseSDT(sec []byte, serviceNames map[int]string, serviceTypes map[int]int) {
	if len(sec) < 11 {
		return
	}
	tableID := sec[0]
	// ETSI EN 300 468: Only table_id 0x42 represents the actual transport stream.
	// 0x46 is "other transport stream" which must not be parsed into the current multiplex.
	if tableID != 0x42 {
		return
	}

	sectionLength := int(binary.BigEndian.Uint16(sec[1:3]) & 0x0FFF)
	if len(sec) < sectionLength+3 {
		return
	}

	offset := 11
	endOffset := sectionLength + 3 - 4 // minus CRC
	if endOffset > len(sec) {
		endOffset = len(sec)
	}

	for offset+5 <= endOffset {
		serviceID := int(binary.BigEndian.Uint16(sec[offset : offset+2]))
		descriptorsLoopLength := int(binary.BigEndian.Uint16(sec[offset+3:offset+5]) & 0x0FFF)
		offset += 5

		descEnd := offset + descriptorsLoopLength
		if descEnd > endOffset {
			break
		}

		for d := offset; d+2 <= descEnd; {
			tag := sec[d]
			length := int(sec[d+1])
			d += 2

			if d+length > descEnd {
				break
			}

			// Service Descriptor Tag 0x48
			if tag == 0x48 && length >= 3 {
				data := sec[d : d+length]
				serviceTypes[serviceID] = int(data[0])
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

			d += length
		}
		offset = descEnd
	}
}
