package tuner

import (
	"strings"
)

// ParseAITSection parses an ETSI TS 102 809 Application Information Table (AIT) section (table_id 0x74)
// directly extracted from the DVB broadcast signal and returns the full HbbTV URL if present.
func ParseAITSection(sec []byte) string {
	if len(sec) < 16 {
		return ""
	}
	if sec[0] != 0x74 { // table_id must be 0x74 (AIT)
		return ""
	}

	secLen := (int(sec[1]&0x0F) << 8) | int(sec[2])
	if len(sec) < secLen+3 {
		return ""
	}
	totalLen := secLen + 3
	if totalLen > len(sec) {
		totalLen = len(sec)
	}

	// Bytes 8-9: common_descriptors_length
	commonDescLen := (int(sec[8]&0x0F) << 8) | int(sec[9])
	appLoopOffset := 10 + commonDescLen
	if appLoopOffset+2 > totalLen-4 { // -4 for CRC32
		return ""
	}

	appLoopLen := (int(sec[appLoopOffset]&0x0F) << 8) | int(sec[appLoopOffset+1])
	appEnd := appLoopOffset + 2 + appLoopLen
	if appEnd > totalLen-4 {
		appEnd = totalLen - 4
	}

	curr := appLoopOffset + 2
	for curr+9 <= appEnd {
		// app entry: org_id (4), app_id (2), control_code (1), descriptors_loop_length (2)
		descLoopLen := (int(sec[curr+7]&0x0F) << 8) | int(sec[curr+8])
		descStart := curr + 9
		descEnd := descStart + descLoopLen
		if descEnd > appEnd {
			descEnd = appEnd
		}

		var urlBase string
		var initialPath string

		for d := descStart; d+2 <= descEnd; {
			dtag := sec[d]
			dlen := int(sec[d+1])
			dData := d + 2
			dNext := dData + dlen
			if dNext > descEnd {
				break
			}

			// Tag 0x02 = transport_protocol_descriptor (ETSI TS 102 809)
			if dtag == 0x02 && dlen >= 4 {
				protocolID := (int(sec[dData]) << 8) | int(sec[dData+1])
				// 0x0003 = HTTP / HTTPS protocol
				if protocolID == 0x0003 && dlen >= 5 && urlBase == "" {
					urlBaseLen := int(sec[dData+3])
					if dData+4+urlBaseLen <= dNext {
						urlBase = string(sec[dData+4 : dData+4+urlBaseLen])
					}
				}
			} else if dtag == 0x15 && dlen > 0 && initialPath == "" { // Tag 0x15 = simple_application_location_descriptor
				initialPath = string(sec[dData:dNext])
			}

			d = dNext
		}

		if urlBase != "" {
			urlBase = strings.TrimSpace(urlBase)
			initialPath = strings.TrimSpace(initialPath)
			if strings.HasSuffix(urlBase, "/") && strings.HasPrefix(initialPath, "/") {
				return urlBase + initialPath[1:]
			} else if !strings.HasSuffix(urlBase, "/") && !strings.HasPrefix(initialPath, "/") && initialPath != "" {
				return urlBase + "/" + initialPath
			}
			return urlBase + initialPath
		}

		curr = descEnd
	}

	return ""
}
