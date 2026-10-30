package epg

import (
	"encoding/binary"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dvb-web/internal/db"
	"golang.org/x/text/encoding/charmap"
)

// Parser handles extracting EPG data from MPEG-TS streams
type Parser struct {
	db             *db.Database
	mu             sync.Mutex
	pendingSection []byte
	onUpdate       func(events []db.EPGEvent)
}

func NewParser(database *db.Database, onUpdate func(events []db.EPGEvent)) *Parser {
	return &Parser{
		db:       database,
		onUpdate: onUpdate,
	}
}

// FeedTSChunk processes an in-memory chunk of TS packets without any device I/O
func (p *Parser) FeedTSChunk(chunk []byte, channelMap map[int]int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i+188 <= len(chunk); i += 188 {
		pkt := chunk[i : i+188]
		if pkt[0] != 0x47 { // TS sync byte
			continue
		}

		// PID is 13 bits
		pid := (int(pkt[1]&0x1F) << 8) | int(pkt[2])
		payloadUnitStart := (pkt[1] & 0x40) != 0

		// EIT PID is 0x0012 (18)
		if pid == 0x0012 {
			payload := pkt[4:] // skip TS header

			// Check adaptation field
			adaptation := (pkt[3] >> 4) & 0x03
			if adaptation == 2 {
				continue // adaptation field only
			} else if adaptation == 3 {
				adaptLen := int(payload[0])
				if adaptLen+1 < len(payload) {
					payload = payload[adaptLen+1:]
				} else {
					continue
				}
			}

			if payloadUnitStart {
				if len(p.pendingSection) > 0 {
					p.processEITSection(p.pendingSection, channelMap)
					p.pendingSection = nil
				}
				if len(payload) > 0 {
					pointerField := int(payload[0])
					if pointerField+1 < len(payload) {
						p.pendingSection = append(p.pendingSection, payload[pointerField+1:]...)
					}
				}
			} else if len(p.pendingSection) > 0 {
				p.pendingSection = append(p.pendingSection, payload...)
			}
		}
	}
}

func (p *Parser) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pendingSection = nil
}

// processEITSection parses a complete PSI EIT section
func (p *Parser) processEITSection(sec []byte, channelMap map[int]int64) {
	if len(sec) < 14 {
		return
	}

	tableID := sec[0]
	// 0x4E = actual TS present/following; 0x4F = other TS present/following
	// 0x50..0x5F = schedule
	if tableID < 0x4E || (tableID > 0x4F && (tableID < 0x50 || tableID > 0x6F)) {
		return
	}

	sectionLength := int(binary.BigEndian.Uint16(sec[1:3]) & 0x0FFF)
	if len(sec) < sectionLength+3 {
		return
	}

	serviceID := int(binary.BigEndian.Uint16(sec[3:5]))
	channelDBID, exists := channelMap[serviceID]
	if !exists {
		return
	}

	// Payload starts at byte 14, ends before 4-byte CRC32
	offset := 14
	endOffset := sectionLength + 3 - 4

	var events []db.EPGEvent

	for offset+12 <= endOffset {
		eventID := int(binary.BigEndian.Uint16(sec[offset : offset+2]))
		startTime := parseMJDBCD(sec[offset+2 : offset+7])
		duration := parseBCDDuration(sec[offset+7 : offset+10])
		endTime := startTime.Add(duration)

		descriptorsLoopLength := int(binary.BigEndian.Uint16(sec[offset+10:offset+12]) & 0x0FFF)
		offset += 12

		descEnd := offset + descriptorsLoopLength
		if descEnd > endOffset {
			break
		}

		// Discard corrupt packets, absurd years, or placeholder blocks > 24 hours
		if startTime.IsZero() || startTime.Year() < 2024 || startTime.Year() > 2035 || duration <= 0 || duration > 24*time.Hour {
			offset = descEnd
			continue
		}

		titles := make(map[string]string)
		shortDescriptions := make(map[string]string)
		extDescriptions := make(map[string]string)

		// Loop through descriptors
		for offset+2 <= descEnd {
			descTag := sec[offset]
			descLen := int(sec[offset+1])
			offset += 2

			if offset+descLen > descEnd {
				break
			}

			data := sec[offset : offset+descLen]

			switch descTag {
			case 0x4D: // Short Event Descriptor
				if len(data) >= 5 {
					lang := normalizeLang(string(data[0:3])) // ISO-639-2 (e.g. "eng", "ell", "fra")
					if lang != "" {
						nameLen := int(data[3])
						if 4+nameLen <= len(data) {
							title := DecodeDVBString(data[4 : 4+nameLen])
							textOffset := 4 + nameLen
							if textOffset < len(data) {
								textLen := int(data[textOffset])
								if textOffset+1+textLen <= len(data) {
									desc := DecodeDVBString(data[textOffset+1 : textOffset+1+textLen])
									if desc != "" {
										shortDescriptions[lang] = desc
									}
								}
							}
							titles[lang] = title
						}
					}
				}
			case 0x4E: // Extended Event Descriptor (ETSI EN 300 468)
				if len(data) >= 6 {
					lang := normalizeLang(string(data[1:4]))
					if lang != "" {
						lengthOfItems := int(data[4])
						textOffset := 5 + lengthOfItems
						if textOffset < len(data) {
							textLen := int(data[textOffset])
							start := textOffset + 1
							end := start + textLen
							if end > len(data) {
								end = len(data)
							}
							if start <= len(data) {
								text := DecodeDVBString(data[start:end])
								if text != "" {
									// In DVB SI, consecutive extended event descriptors for the same language
									// are chunks of the continuous text description split across 248 bytes.
									// They must be concatenated directly without adding line breaks.
									extDescriptions[lang] = extDescriptions[lang] + text
								}
							}
						}
					}
				}
			}

			offset += descLen
		}

		descriptions := make(map[string]string)
		for lang, shortDesc := range shortDescriptions {
			if shortDesc != "" {
				descriptions[lang] = shortDesc
			}
		}
		for lang, extDesc := range extDescriptions {
			if extDesc != "" {
				if shortDesc, exists := descriptions[lang]; exists && shortDesc != "" && shortDesc != extDesc {
					if !strings.HasPrefix(extDesc, shortDesc) && !strings.Contains(extDesc, shortDesc) {
						descriptions[lang] = shortDesc + " - " + extDesc
					} else {
						descriptions[lang] = extDesc
					}
				} else {
					descriptions[lang] = extDesc
				}
			}
		}

		if len(titles) > 0 && !startTime.IsZero() {
			events = append(events, db.EPGEvent{
				ChannelID:    channelDBID,
				EventID:      eventID,
				StartTime:    startTime,
				EndTime:      endTime,
				Titles:       titles,
				Descriptions: descriptions,
			})
		}
	}

	if len(events) > 0 {
		_ = p.db.SaveEPGEvents(events)
		if p.onUpdate != nil {
			p.onUpdate(events)
		}
	}
}

// parseMJDBCD decodes DVB 5-byte Modified Julian Date + UTC time
func parseMJDBCD(b []byte) time.Time {
	if len(b) < 5 {
		return time.Time{}
	}
	mjd := binary.BigEndian.Uint16(b[0:2])
	if mjd == 0 {
		return time.Time{}
	}

	// MJD to Calendar Date conversion algorithm
	yPrime := int((float64(mjd) - 15078.2) / 365.25)
	mPrime := int(float64(float64(mjd)-14956.1-float64(int(float64(yPrime)*365.25))) / 30.6001)
	day := int(mjd) - 14956 - int(float64(yPrime)*365.25) - int(float64(mPrime)*30.6001)
	k := 0
	if mPrime == 14 || mPrime == 15 {
		k = 1
	}
	year := yPrime + k + 1900
	month := mPrime - 1 - k*12

	hour := bcdToInt(b[2])
	min := bcdToInt(b[3])
	sec := bcdToInt(b[4])

	return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
}

func parseBCDDuration(b []byte) time.Duration {
	if len(b) < 3 {
		return 0
	}
	hours := bcdToInt(b[0])
	minutes := bcdToInt(b[1])
	seconds := bcdToInt(b[2])
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second
}

func bcdToInt(b byte) int {
	return int((b>>4)&0x0F)*10 + int(b&0x0F)
}

func normalizeLang(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	if l == "ell" {
		return "gre"
	}
	if len(l) == 3 && l[0] >= 'a' && l[0] <= 'z' && l[1] >= 'a' && l[1] <= 'z' && l[2] >= 'a' && l[2] <= 'z' {
		return l
	}
	return ""
}

// DecodeDVBString decodes text according to ETSI EN 300 468 Annex A
func DecodeDVBString(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	first := data[0]

	// 0x15 = explicit UTF-8 encoding
	if first == 0x15 {
		return cleanDVBString(string(data[1:]))
	}

	// 0x1F = encoding defined by encoding_type_id
	if first == 0x1F && len(data) > 2 {
		return DecodeDVBString(data[2:])
	}

	// 0x10 = 3-byte encoding table selector
	if first == 0x10 && len(data) >= 3 {
		table := binary.BigEndian.Uint16(data[1:3])
		raw := data[3:]
		switch table {
		case 7:
			// ISO-8859-7 (Latin/Greek)
			return decodeWithCharmap(raw, charmap.ISO8859_7)
		case 1:
			return decodeWithCharmap(raw, charmap.ISO8859_1)
		case 2:
			return decodeWithCharmap(raw, charmap.ISO8859_2)
		case 5:
			return decodeWithCharmap(raw, charmap.ISO8859_5)
		case 9:
			return decodeWithCharmap(raw, charmap.ISO8859_9)
		case 15:
			return decodeWithCharmap(raw, charmap.ISO8859_15)
		default:
			return decodeWithCharmap(raw, charmap.ISO8859_7)
		}
	}

	// 0x01..0x0B = Single-byte table selector
	if first >= 0x01 && first <= 0x0B {
		raw := data[1:]
		switch first {
		case 0x01: // ISO-8859-5 (Cyrillic)
			return decodeWithCharmap(raw, charmap.ISO8859_5)
		case 0x02: // ISO-8859-6 (Arabic)
			return decodeWithCharmap(raw, charmap.ISO8859_6)
		case 0x03: // ISO-8859-7 (Latin/Greek)
			return decodeWithCharmap(raw, charmap.ISO8859_7)
		case 0x04: // ISO-8859-8 (Hebrew)
			return decodeWithCharmap(raw, charmap.ISO8859_8)
		case 0x05: // ISO-8859-9 (Latin 5)
			return decodeWithCharmap(raw, charmap.ISO8859_9)
		case 0x09: // ISO-8859-13
			return decodeWithCharmap(raw, charmap.ISO8859_13)
		case 0x0B: // ISO-8859-15
			return decodeWithCharmap(raw, charmap.ISO8859_15)
		default:
			return decodeWithCharmap(raw, charmap.ISO8859_7)
		}
	}

	// 0x11 = UCS-2 Big Endian (ISO/IEC 10646)
	if first == 0x11 && len(data) > 1 {
		raw := data[1:]
		var runes []rune
		for i := 0; i+1 < len(raw); i += 2 {
			u := binary.BigEndian.Uint16(raw[i : i+2])
			runes = append(runes, rune(u))
		}
		return cleanDVBString(string(runes))
	}

	// Default fallback (first byte >= 0x20):
	// Check if already valid UTF-8 containing non-ASCII bytes
	if utf8.Valid(data) {
		hasHigh := false
		for _, b := range data {
			if b >= 0x80 {
				hasHigh = true
				break
			}
		}
		if hasHigh {
			return cleanDVBString(string(data))
		}
	}

	// In DVB standard, default without prefix is ISO 6937, but in Greek broadcasts (ERT, Digea),
	// text without prefix is universally transmitted in ISO-8859-7!
	return decodeWithCharmap(data, charmap.ISO8859_7)
}

func decodeWithCharmap(raw []byte, cm *charmap.Charmap) string {
	// Pre-process DVB control characters:
	// 0x8A is CR/LF newline in DVB ETSI EN 300 468
	filtered := make([]byte, 0, len(raw))
	for _, b := range raw {
		if b == 0x8A {
			filtered = append(filtered, '\n')
		} else if b >= 0x80 && b <= 0x9F {
			// Skip DVB formatting/emphasis characters
			continue
		} else {
			filtered = append(filtered, b)
		}
	}

	dec := cm.NewDecoder()
	res, err := dec.Bytes(filtered)
	if err == nil {
		return cleanDVBString(string(res))
	}

	// Fallback byte-by-byte
	var runes []rune
	for _, b := range filtered {
		r := cm.DecodeByte(b)
		if r != utf8.RuneError {
			runes = append(runes, r)
		} else if b >= 32 || b == '\n' || b == '\t' {
			runes = append(runes, rune(b))
		}
	}
	return cleanDVBString(string(runes))
}

func cleanDVBString(s string) string {
	// Strip non-printable DVB emphasis/formatting characters (< 0x20 except tab/newline)
	var out []rune
	for _, r := range s {
		if r >= 32 || r == '\n' || r == '\t' {
			out = append(out, r)
		}
	}
	return string(out)
}

// SeedMockEPG provides sample multi-language EPG data for testing/mock mode
func SeedMockEPG(database *db.Database, channels []db.Channel) {
	now := time.Now().Truncate(time.Hour)
	var events []db.EPGEvent

	sampleShows := []struct {
		titles map[string]string
		descs  map[string]string
	}{
		{
			titles: map[string]string{
				"eng": "Global News Today",
				"gre": "Παγκόσμιες Ειδήσεις Σήμερα",
				"fra": "Nouvelles du Monde",
				"deu": "Weltnachrichten Heute",
			},
			descs: map[string]string{
				"eng": "Comprehensive coverage of international breaking news, politics, and economy.",
				"gre": "Ολοκληρωμένη κάλυψη των διεθνών ειδήσεων, πολιτική και οικονομία.",
				"fra": "Couverture complète de l'actualité internationale.",
				"deu": "Umfassende Berichterstattung über internationale Nachrichten.",
			},
		},
		{
			titles: map[string]string{
				"eng": "Nature Documentary: Oceans",
				"gre": "Ντοκιμαντέρ Φύσης: Ωκεανοί",
				"fra": "Documentaire Nature: Les Océans",
				"deu": "Naturdoku: Die Ozeane",
			},
			descs: map[string]string{
				"eng": "Exploring the deepest mysteries of marine ecosystems.",
				"gre": "Εξερευνώντας τα βαθύτερα μυστήρια των θαλάσσιων οικοσυστημάτων.",
				"fra": "Exploration des mystères les plus profonds des écosystèmes marins.",
				"deu": "Erforschung der tiefsten Geheimnisse der Meeresökosysteme.",
			},
		},
		{
			titles: map[string]string{
				"eng": "Evening Movie: Cyber Horizon",
				"gre": "Βραδινή Ταινία: Cyber Horizon",
				"fra": "Film du Soir: Horizon Cyber",
				"deu": "Abendfilm: Cyber Horizon",
			},
			descs: map[string]string{
				"eng": "Sci-fi thriller set in a futuristic metropolis.",
				"gre": "Θρίλερ επιστημονικής φαντασίας σε μια φουτουριστική μητρόπολη.",
				"fra": "Thriller de science-fiction dans une métropole futuriste.",
				"deu": "Science-Fiction-Thriller in einer futuristischen Metropole.",
			},
		},
	}

	for _, c := range channels {
		for i, show := range sampleShows {
			start := now.Add(time.Duration(i) * time.Hour)
			end := start.Add(1 * time.Hour)
			events = append(events, db.EPGEvent{
				ChannelID:    c.ID,
				EventID:      100 + i,
				StartTime:    start,
				EndTime:      end,
				Titles:       show.titles,
				Descriptions: show.descs,
			})
		}
	}

	if err := database.SaveEPGEvents(events); err != nil {
		log.Printf("[EPG] Failed to seed mock EPG: %v", err)
	} else {
		log.Printf("[EPG] Seeded mock EPG with %d events", len(events))
	}
}
