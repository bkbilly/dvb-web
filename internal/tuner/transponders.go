package tuner

type Transponder struct {
	Frequency      uint32 `json:"frequency"`       // Frequency in Hz
	Bandwidth      uint32 `json:"bandwidth"`       // Bandwidth in Hz (e.g. 8000000)
	DeliverySystem string `json:"delivery_system"` // "DVBT" or "DVBT2"
	Description    string `json:"description,omitempty"`
}

type TransmitterSite struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Transponders []Transponder `json:"transponders"`
}

type CountryRegions struct {
	Code         string            `json:"code"`
	Name         string            `json:"name"`
	Flag         string            `json:"flag"`
	Transmitters []TransmitterSite `json:"transmitters"`
}

type ScanConfig struct {
	AdapterID      int           `json:"adapter_id"`
	Mode           string        `json:"mode"` // "region", "manual", "sweep"
	CountryCode    string        `json:"country_code,omitempty"`
	TransmitterID  string        `json:"transmitter_id,omitempty"`
	Transponders   []Transponder `json:"transponders,omitempty"`
	DefaultBW      uint32        `json:"default_bw,omitempty"`
	DeliverySystem string        `json:"delivery_system,omitempty"`
}

var predefinedRegions = []CountryRegions{
	{
		Code: "GB",
		Name: "United Kingdom",
		Flag: "🇬🇧",
		Transmitters: []TransmitterSite{
			{
				ID:   "gb-london",
				Name: "London - Crystal Palace",
				Transponders: []Transponder{
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "PSB2 (D3&4)"},
					{Frequency: 498000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM4 (SDN)"},
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM6 (Arcq B)"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM5 (Arcq A)"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "PSB1 (BBC A)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "PSB3 (BBC B HD)"},
				},
			},
			{
				ID:   "gb-manchester",
				Name: "Manchester - Winter Hill",
				Transponders: []Transponder{
					{Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "PSB1 (BBC A)"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "PSB2 (D3&4)"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "PSB3 (BBC B HD)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM4 (SDN)"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM5 (Arcq A)"},
					{Frequency: 594000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM6 (Arcq B)"},
				},
			},
			{
				ID:   "gb-birmingham",
				Name: "Birmingham - Sutton Coldfield",
				Transponders: []Transponder{
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "PSB2 (D3&4)"},
					{Frequency: 626000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "PSB3 (BBC B HD)"},
					{Frequency: 642000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "PSB1 (BBC A)"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM4 (SDN)"},
					{Frequency: 666000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM5 (Arcq A)"},
					{Frequency: 682000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "COM6 (Arcq B)"},
				},
			},
			{
				ID:   "gb-all",
				Name: "United Kingdom - All National Frequencies",
				Transponders: []Transponder{
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 498000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 594000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 626000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 642000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 666000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 682000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
				},
			},
		},
	},
	{
		Code: "GR",
		Name: "Greece",
		Flag: "🇬🇷",
		Transmitters: []TransmitterSite{
			{
				ID:   "gr-athens-ymittos",
				Name: "Athens - Ymittos / Parnitha",
				Transponders: []Transponder{
					{Frequency: 474000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ERT Mux 1 (Ch 21)"},
					{Frequency: 482000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Digea Mux 1 (Ch 22)"},
					{Frequency: 522000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Digea Mux 2 (Ch 27)"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Attica Regional 1 (Ch 28)"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Attica Regional 2 (Ch 30)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ERT Mux 2 (Ch 34)"},
				},
			},
			{
				ID:   "gr-thessaloniki",
				Name: "Thessaloniki - Chortiatis",
				Transponders: []Transponder{
					{Frequency: 474000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ERT Mux 1 (Ch 21)"},
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Digea Mux 1 (Ch 23)"},
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Digea Mux 2 (Ch 26)"},
					{Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Central Macedonia 1 (Ch 29)"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Central Macedonia 2 (Ch 32)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ERT Mux 2 (Ch 34)"},
				},
			},
			{
				ID:   "gr-all",
				Name: "Greece - All Digea & ERT Multiplexes",
				Transponders: []Transponder{
					{Frequency: 474000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 482000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 498000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 522000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
				},
			},
		},
	},
	{
		Code: "DE",
		Name: "Germany",
		Flag: "🇩🇪",
		Transmitters: []TransmitterSite{
			{
				ID:   "de-berlin",
				Name: "Berlin - Alexanderplatz / Schäferberg",
				Transponders: []Transponder{
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ARD Digital (Ch 25)"},
					{Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "RBB Regional (Ch 33)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ZDF HD (Ch 34)"},
					{Frequency: 626000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Freenet TV 1 (Ch 40)"},
					{Frequency: 642000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Freenet TV 2 (Ch 42)"},
					{Frequency: 682000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Freenet TV 3 (Ch 47)"},
				},
			},
			{
				ID:   "de-munich",
				Name: "Munich - Olympiaturm / Wendelstein",
				Transponders: []Transponder{
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "BR Regional (Ch 26)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ZDF HD (Ch 34)"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "ARD Digital (Ch 35)"},
					{Frequency: 602000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Freenet TV 1 (Ch 37)"},
					{Frequency: 682000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Freenet TV 2 (Ch 47)"},
				},
			},
			{
				ID:   "de-all",
				Name: "Germany - All National Frequencies",
				Transponders: []Transponder{
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 594000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 602000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 626000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 642000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 666000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 682000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
				},
			},
		},
	},
	{
		Code: "FR",
		Name: "France",
		Flag: "🇫🇷",
		Transmitters: []TransmitterSite{
			{
				ID:   "fr-paris",
				Name: "Paris - Tour Eiffel",
				Transponders: []Transponder{
					{Frequency: 482000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "R3 (Ch 22)"},
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "R7 (Ch 25)"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "R6 (Ch 30)"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "R2 (Ch 32)"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "R1 (Ch 35)"},
					{Frequency: 642000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "R4 (Ch 42)"},
				},
			},
			{
				ID:   "fr-all",
				Name: "France - All National Multiplexes",
				Transponders: []Transponder{
					{Frequency: 482000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 586000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 626000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 642000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 682000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
				},
			},
		},
	},
	{
		Code: "ES",
		Name: "Spain",
		Flag: "🇪🇸",
		Transmitters: []TransmitterSite{
			{
				ID:   "es-madrid",
				Name: "Madrid - Torrespaña",
				Transponders: []Transponder{
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "RGE1 (Ch 23)"},
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "MPE1 (Ch 25)"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "MPE2 (Ch 28)"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "MPE4 (Ch 30)"},
					{Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "MPE3 (Ch 33)"},
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "RGE2 (Ch 39)"},
					{Frequency: 634000000, Bandwidth: 8000000, DeliverySystem: "DVBT", Description: "MPE5 (Ch 41)"},
				},
			},
			{
				ID:   "es-all",
				Name: "Spain - All TDT Multiplexes",
				Transponders: []Transponder{
					{Frequency: 490000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 506000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 538000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 570000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 634000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT"},
				},
			},
		},
	},
	{
		Code: "IT",
		Name: "Italy",
		Flag: "🇮🇹",
		Transmitters: []TransmitterSite{
			{
				ID:   "it-rome",
				Name: "Rome - Monte Mario / Monte Cavo",
				Transponders: []Transponder{
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Mediaset 1 (Ch 26)"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "RAI Mux A (Ch 30)"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Persidera 1 (Ch 34)"},
					{Frequency: 594000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Mediaset 2 (Ch 36)"},
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "RAI Mux B (Ch 39)"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT2", Description: "Cairo Due (Ch 43)"},
				},
			},
			{
				ID:   "it-all",
				Name: "Italy - All National Frequencies",
				Transponders: []Transponder{
					{Frequency: 514000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 530000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 546000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 562000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 578000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 594000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 618000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 626000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 650000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
					{Frequency: 666000000, Bandwidth: 8000000, DeliverySystem: "DVBT2"},
				},
			},
		},
	},
	{
		Code: "AU",
		Name: "Australia",
		Flag: "🇦🇺",
		Transmitters: []TransmitterSite{
			{
				ID:   "au-sydney",
				Name: "Sydney - Artarmon / Gore Hill",
				Transponders: []Transponder{
					{Frequency: 177500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "ABC (VHF 6)"},
					{Frequency: 191500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "Nine Network (VHF 8)"},
					{Frequency: 219500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "Seven Network (VHF 10)"},
					{Frequency: 226500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "Network 10 (VHF 11)"},
					{Frequency: 543500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "SBS (UHF 30)"},
				},
			},
			{
				ID:   "au-melbourne",
				Name: "Melbourne - Mount Dandenong",
				Transponders: []Transponder{
					{Frequency: 177500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "ABC (VHF 6)"},
					{Frequency: 191500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "Seven Network (VHF 8)"},
					{Frequency: 219500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "Nine Network (VHF 10)"},
					{Frequency: 226500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "Network 10 (VHF 11)"},
					{Frequency: 536500000, Bandwidth: 7000000, DeliverySystem: "DVBT", Description: "SBS (UHF 29)"},
				},
			},
		},
	},
}

// GetPredefinedRegions returns the list of supported countries and their transmitter sites
func GetPredefinedRegions() []CountryRegions {
	return predefinedRegions
}
