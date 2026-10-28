package tuner

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"dvb-web/internal/db"
	"dvb-web/internal/epg"
)

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
				IsRadio:        isRadio,
				HbbtvURL:       d.serviceHbbTV[sid],
				HasTeletext:    d.serviceTeletext[sid],
				Enabled:        true,
			})
			*nextNum++
		}
	}
	return results
}

// parseSDT parses Service Names and Types from DVB SDT section
func parseSDT(sec []byte, serviceNames map[int]string, serviceTypes map[int]int) {
	if len(sec) < 11 {
		return
	}
	tableID := sec[0]
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
