# DVB-Web

[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![OS - Linux](https://img.shields.io/badge/OS-Linux-blue?logo=linux&logoColor=white)]()
[![Go](https://img.shields.io/badge/Go-1.24+-blue?logo=go&logoColor=white)]()
[![GHCR](https://img.shields.io/badge/GHCR-dvb--web-blue?logo=github&logoColor=white)](https://github.com/bkbilly/dvb-web/pkgs/container/dvb-web)

A fast, lightweight web TV streaming server and DVR for digital broadcast television (DVB-T/T2, DVB-C, DVB-S/S2). Built in pure Go with WebRTC for instant channel zapping, multi-tuner multiplex sharing, and native integration for **Jellyfin**, **Plex**, **Kodi**, and **VLC**.

---

## ✨ Features

- ⚡ **Sub-Second WebRTC Zapping**: Instant video rendering (~150ms) without HLS buffering delay.
- 💤 **Zero-Resource Idle**: Automatically releases tuners and stops FFmpeg when idle (0% CPU and low power).
- 🍿 **Plex & Jellyfin Integration**: Built-in HDHomeRun emulation (UDP discovery on port `65001`), M3U playlist (`/playlist.m3u`), and XMLTV guide (`/epg.xml`).
- 🔀 **Smart Multi-Tuner Pooling**: Multiplex sharing allows multiple viewers to watch channels on the same frequency using a single tuner.
- 🔴 **Integrated DVR**: Schedule future broadcasts from the EPG or record live TV with one click; automatically remuxes to fast-start MP4.
- 📻 **Radio & Multi-Track Audio/Subtitles**: Audio-only radio playback with equalizer display, plus live audio language and subtitle switching.
- 🚀 **Hardware Acceleration**: Automatic detection for Intel/AMD VA-API, NVIDIA NVENC, and Raspberry Pi V4L2 M2M, with CPU fallback.
- 📅 **Live Multi-Language EPG**: Interactive timeline guide with dynamic broadcast EIT table parsing and full program descriptions.

---

## 🚀 Quick Start

### Docker Compose (Recommended)

```yaml
services:
  dvb-web:
    image: ghcr.io/bkbilly/dvb-web:latest
    container_name: dvb-web
    restart: unless-stopped
    network_mode: host # Recommended for WebRTC, UDP streaming & HDHomeRun discovery
    devices:
      - /dev/dvb:/dev/dvb # TV Tuners
      - /dev/dri:/dev/dri # GPU Hardware Acceleration (Intel/AMD)
    volumes:
      - ./data:/app/data
    environment:
      - TZ=UTC
```

```bash
docker compose up -d
```
Open **`http://localhost:8080`** in your browser.

---

### Docker CLI

```bash
docker run -d \
  --name dvb-web \
  --restart unless-stopped \
  --network host \
  --device /dev/dvb:/dev/dvb \
  --device /dev/dri:/dev/dri \
  -v $(pwd)/data:/app/data \
  ghcr.io/bkbilly/dvb-web:latest
```

---

### Build from Source

```bash
# Prerequisites: Linux, Go 1.22+, ffmpeg
go build -o dvb-web cmd/server/main.go
./dvb-web
```

> **No physical tuner attached?** Run `./dvb-web -mock` to test with simulated channels and sample EPG data.

---

## 📺 External Player Integration

| Integration | URL | Compatible Players |
| :--- | :--- | :--- |
| **HDHomeRun Tuner** | `http://<server-ip>:8080` | **Plex** Live TV & DVR, **Jellyfin** HDHomeRun |
| **M3U Playlist** | `http://<server-ip>:8080/playlist.m3u` | **Jellyfin**, **VLC**, **Kodi**, **TiviMate** |
| **XMLTV Guide** | `http://<server-ip>:8080/epg.xml` | **Jellyfin**, **Plex** |

- **Plex**: Go to **Settings** &rarr; **Live TV & DVR** &rarr; **Set up Plex DVR**. Enter `http://<server-ip>:8080`.
- **Jellyfin**: Go to **Dashboard** &rarr; **Live TV** &rarr; add **HDHomeRun** (`http://<server-ip>:8080`) and **XMLTV** (`http://<server-ip>:8080/epg.xml`).

---

## ⌨️ Keyboard Shortcuts

| Key | Action |
| :--- | :--- |
| `↑` / `↓` or `PageUp` / `PageDown` | Previous / Next Channel |
| `0` – `9` | Direct Channel Number Input |
| `F` or `Double-Click Video` | Toggle Fullscreen |
| `M` | Mute / Unmute Audio |

---

## 📄 License & Credits

- Created and maintained by [Vasilis Koulis (bkbilly)](https://github.com/bkbilly).
- Developed in collaboration with LLMs.
- Licensed under the [MIT License](LICENSE).
