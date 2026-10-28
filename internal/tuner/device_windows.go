//go:build windows

package tuner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"dvb-web/internal/epg"

	"golang.org/x/sys/windows/registry"
)

type bdaDeviceInfo struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// findBDARunner locates bda-runner.exe or tsp.exe
func findBDARunner() string {
	// 1. Check directory of current executable
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		for _, name := range []string{"bda-runner.exe", "bda-helper.exe"} {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	// 2. Check current working directory
	for _, name := range []string{"bda-runner.exe", "bda-helper.exe"} {
		if fi, err := os.Stat(name); err == nil && !fi.IsDir() {
			return name
		}
	}
	// 3. Check system PATH
	for _, name := range []string{"bda-runner.exe", "bda-helper.exe", "tsp.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func discoverPlatformTuners(basePort int, ep *epg.Parser) []*Tuner {
	var tuners []*Tuner
	portCounter := basePort

	runnerPath := findBDARunner()
	var bdaList []bdaDeviceInfo

	// 1. Query bda-runner --list if available
	if runnerPath != "" && strings.Contains(strings.ToLower(runnerPath), "bda-") {
		cmd := exec.Command(runnerPath, "--list")
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			_ = json.Unmarshal(out, &bdaList)
		}
	}

	// 2. If bda-runner returned devices, use them
	if len(bdaList) > 0 {
		for _, dev := range bdaList {
			t := &Tuner{
				ID:             dev.Index,
				Name:           dev.Name,
				FrontendPath:   fmt.Sprintf("bda://%d", dev.Index),
				IsBDA:          true,
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
			log.Printf("[TunerPool] Discovered Windows BDA tuner: %s (via %s)", t.Name, filepath.Base(runnerPath))
		}
		return tuners
	}

	// 3. Fallback: Query Windows Registry for BDA Network Tuner devices
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\DeviceClasses\{71985F48-1CA1-11d3-9CC8-00C04F7971E0}`, registry.READ)
	if err == nil {
		defer k.Close()
		subkeys, _ := k.ReadSubKeyNames(-1)
		for i, sub := range subkeys {
			friendlyName := ""
			// Check subkey / # / Device Parameters
			paramPath := `SYSTEM\CurrentControlSet\Control\DeviceClasses\{71985F48-1CA1-11d3-9CC8-00C04F7971E0}\` + sub + `\#\Device Parameters`
			if pk, err := registry.OpenKey(registry.LOCAL_MACHINE, paramPath, registry.READ); err == nil {
				if fn, _, err := pk.GetStringValue("FriendlyName"); err == nil && fn != "" {
					friendlyName = fn
				}
				pk.Close()
			}
			// Check Enum path if encoded in subkey (e.g. ##?#USB#VID_1F4D&PID_A803...#...)
			if friendlyName == "" {
				parts := strings.Split(sub, "#")
				if len(parts) >= 4 {
					enumPath := `SYSTEM\CurrentControlSet\Enum\` + parts[1] + `\` + parts[2] + `\` + parts[3]
					if ek, err := registry.OpenKey(registry.LOCAL_MACHINE, enumPath, registry.READ); err == nil {
						if fn, _, err := ek.GetStringValue("FriendlyName"); err == nil && fn != "" {
							friendlyName = fn
						} else if dd, _, err := ek.GetStringValue("DeviceDesc"); err == nil && dd != "" {
							friendlyName = dd
						}
						ek.Close()
					}
				}
			}
			if friendlyName == "" {
				friendlyName = fmt.Sprintf("BDA TV Tuner %d", i)
			}

			t := &Tuner{
				ID:             i,
				Name:           friendlyName,
				FrontendPath:   fmt.Sprintf("bda://%d", i),
				IsBDA:          true,
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
			log.Printf("[TunerPool] Discovered Windows BDA tuner: %s (Device ID: %d)", t.Name, t.ID)
		}
	}

	return tuners
}

func startBDATSPump(ctx context.Context, t *Tuner, freqHz int64, bwHz int, tsQueue chan<- []byte, parseQueue chan<- []byte) error {
	runner := findBDARunner()
	if runner == "" {
		return errors.New("bda-runner.exe or tsp.exe not found on system. Please place bda-runner.exe next to dvb-web.exe or install TSDuck")
	}

	var cmd *exec.Cmd
	if strings.Contains(strings.ToLower(runner), "tsp") {
		// TSDuck format
		cmd = exec.CommandContext(ctx, runner,
			"-I", "dvb",
			"--frequency", strconv.FormatInt(freqHz, 10),
			"--bandwidth", strconv.Itoa(bwHz),
			"-O", "file", "-")
	} else {
		// bda-runner format
		cmd = exec.CommandContext(ctx, runner,
			"--tuner", strconv.Itoa(t.ID),
			"--freq", strconv.FormatInt(freqHz, 10),
			"--bw", strconv.Itoa(bwHz))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to open BDA runner stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start BDA runner: %w", err)
	}

	log.Printf("[BDA] Started BDA stream on %s (Freq: %.2f MHz, BW: %d MHz)", t.Name, float64(freqHz)/1e6, bwHz/1e6)

	go func() {
		defer close(tsQueue)
		defer close(parseQueue)
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			log.Printf("[BDA] Stopped BDA stream on %s", t.Name)
		}()

		readBuf := make([]byte, 188*256) // 48KB
		var leftover []byte

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, err := stdout.Read(readBuf)
			if err != nil {
				if err != io.EOF {
					log.Printf("[BDA] Read error: %v", err)
				}
				return
			}
			if n <= 0 {
				continue
			}

			cleanData, newLeftover := SanitizeAndSyncTS(readBuf[:n], leftover)
			leftover = newLeftover

			if len(cleanData) == 0 {
				continue
			}

			select {
			case tsQueue <- cleanData:
			default:
			}

			select {
			case parseQueue <- cleanData:
			default:
			}
		}
	}()

	return nil
}

func openFrontendDevice(path string) (int, error) {
	return -1, errWindowsDVBUnsupported
}

func openDVRDevice(path string) (int, error) {
	return -1, errWindowsDVBUnsupported
}

func closeDeviceFD(fd int) error {
	return nil
}

func drainDVRDevice(fd int, buf []byte) (int, error) {
	return 0, nil
}

func pollAndReadDVR(ctx context.Context, dvrFD int, buf []byte) (int, error) {
	return 0, errors.New("DVR polling not supported on Windows")
}
