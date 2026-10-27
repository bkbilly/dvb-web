package tuner

// SignalInfo represents tuner RF signal quality, metrics and lock status
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
