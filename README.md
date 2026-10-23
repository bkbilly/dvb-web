[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![OS - Linux](https://img.shields.io/badge/OS-Linux-blue?logo=linux&logoColor=white)]()
[![Go](https://img.shields.io/badge/Go-1.24+-blue?logo=go&logoColor=white)]()
[![Docker](https://img.shields.io/docker/v/bkbillybk/dvb-web?logo=docker&logoColor=white&label=Docker&color=blue)](https://hub.docker.com/r/bkbillybk/dvb-web)
[![Docker Pulls](https://img.shields.io/docker/pulls/bkbillybk/dvb-web?logo=docker&logoColor=white&color=blue)](https://hub.docker.com/r/bkbillybk/dvb-web)

# DVB-Web

A modern, fast, zero-resource web application and streaming server for DVB television (DVB-T/T2, DVB-C, DVB-S/S2). Built in pure Go with WebRTC for instant channel zapping, multi-tuner multiplex sharing, and native integration for **Jellyfin**, **Plex**, **Kodi**, and **VLC**.

---

## ✨ Features

- ⚡ **Sub-Second Channel Zapping**: Powered by pure Go WebRTC (**Pion**). Video renders in ~100–200ms with no HLS buffering latency.
- 💤 **Zero Resource Idle**: Automatically closes tuner hardware handles and terminates background FFmpeg transcoding when no viewers are watching (**0.0% CPU usage and power-saving**).
- 🍿 **Media Server Integration (Jellyfin & Plex)**:
  - **Native HDHomeRun Emulation**: Auto-discovered by Plex Live TV & DVR and Jellyfin with zero setup (UDP `65001` + `/discover.json` + `/lineup.json`).
  - **M3U Playlist (`/playlist.m3u`)**: 1-click import into Jellyfin M3U Tuner, Kodi, and VLC.
  - **XMLTV Guide (`/epg.xml`)**: Clean, deduplicated 7-day electronic program guide export.
- 🔀 **Smart Multi-Tuner Pool & Multiplex Sharing**:
  - Automatically discovers all `/dev/dvb/adapter*` hardware tuners.
  - Channels broadcast on the same frequency multiplex share a single tuner across multiple viewers without retuning.
  - Conflict resolution dialog shows active viewers with force-kick capability when all tuners are busy.
- 🌐 **Multi-Language Electronic Program Guide (EPG)**:
  - Live on-the-fly decoding of broadcast EIT tables (PID `0x0012` - ISO-639-2 tags).
  - Continuous multi-descriptor synopsis parsing without artificial 248-character text splits.
  - Interactive modern timeline guide with day switcher and live program tracking.
- 🚀 **Universal Hardware Acceleration**:
  - Automatic detection and support for **Intel/AMD VA-API** (`/dev/dri`), **NVIDIA NVENC**, and **Raspberry Pi V4L2 M2M**.
  - Seamless CPU fallback (`libx264 ultrafast`).
- 📱 **Mobile & Desktop Responsive**:
  - Fully responsive layout with dynamic video height adapting to 16:9 and 4:3 broadcast aspect ratios.
  - Touch-friendly controls, double-click fullscreen, and auto-hiding OSD banner.
- 🔍 **Channel Scanner & Pruner**:
  - Fast background frequency scanner with diff summary of new and missing channels.
- 💾 **Embedded Pure Go Database**:
  - Built-in SQLite (`modernc.org/sqlite`) with automatic schema migrations. Zero CGO and zero external database setup required.

---

## 🚀 Quick Start

### Run with Docker Compose (Recommended)

```yaml
# docker-compose.yml
services:
  dvb-web:
    image: bkbillybk/dvb-web:latest
    container_name: dvb-web
    restart: unless-stopped
    network_mode: host # Recommended for WebRTC, UDP streaming & HDHomeRun discovery
    devices:
      - /dev/dvb:/dev/dvb # TV Tuners
      - /dev/dri:/dev/dri # GPU (Intel/AMD VA-API)
    volumes:
      - ./data:/app/data
    environment:
      - TZ=UTC
```

Start the container:
```bash
docker compose up -d
```

Open `http://localhost:8080` in your browser.

---

### Run with Docker CLI

```bash
docker run -d \
  --name dvb-web \
  --restart unless-stopped \
  --network host \
  --device /dev/dvb:/dev/dvb \
  --device /dev/dri:/dev/dri \
  -v $(pwd)/data:/app/data \
  bkbillybk/dvb-web:latest
```

---

### Run from Source

```bash
# Prerequisites: Linux, Go 1.22+, ffmpeg installed
go build -o dvb-web cmd/server/main.go
./dvb-web -port 8080 -data-dir ./data -web-dir ./web
```

> **Testing without a physical antenna/tuner?**  
> Run `./dvb-web -mock` to enable the virtual tuner with simulated digital TV test patterns and sample multi-language EPG data.

---

## 📺 External Player Integration (Jellyfin, Plex, Kodi, VLC)

| Integration | URL | Use Case |
| :--- | :--- | :--- |
| **HDHomeRun Tuner** | `http://<server-ip>:8080` | Native tuner for **Plex** Live TV & DVR and **Jellyfin** HDHomeRun |
| **M3U Playlist** | `http://<server-ip>:8080/playlist.m3u` | Channel playlist for **Jellyfin** (M3U Tuner), **VLC**, and **Kodi** |
| **XMLTV Guide** | `http://<server-ip>:8080/epg.xml` | 7-day electronic program guide for **Jellyfin** & **Plex** |

### Plex Setup
1. In Plex Web, go to **Settings** &rarr; **Live TV & DVR** &rarr; **Set up Plex DVR**.
2. Plex will auto-discover **`dvb-web`** on your network (or enter `http://<server-ip>:8080` manually).
3. Select your channels and assign the guide data.

### Jellyfin Setup
1. In Jellyfin, go to **Dashboard** &rarr; **Live TV** &rarr; **Tuner Devices** &rarr; **+**.
2. Choose **HDHomeRun** and enter `http://<server-ip>:8080` (or choose **M3U Tuner** with `http://<server-ip>:8080/playlist.m3u`).
3. Under **TV Guide Data Providers**, click **+** &rarr; **XMLTV** and enter `http://<server-ip>:8080/epg.xml`.

---

## ⌨️ Remote Control & Keyboard Shortcuts

| Key | Action |
| :--- | :--- |
| `↑` / `PageUp` | Previous Channel |
| `↓` / `PageDown` | Next Channel |
| `0` - `9` | Channel Number Direct Zap |
| `F` | Toggle Fullscreen (standalone key) |
| `Double Click` | Toggle Fullscreen on video |
| `M` | Mute / Unmute Audio |

---

## 🔌 API Endpoints

- `GET /api/status`: Tuner status, active viewers, GPU encoder, EPG languages.
- `GET /api/channels`: List channels with current and upcoming show details.
- `POST /api/channels`: Add or edit channel parameters.
- `POST /api/channels/reorder`: Persist channel ordering.
- `POST /api/channels/:id/logo`: Upload custom channel logo.
- `POST /api/scan`: Trigger channel frequency scan.
- `POST /api/scan/prune`: Delete missing channels identified in scan.
- `GET /api/epg?channel_id=X`: Get EPG timeline for a channel.
- `POST /api/tuners/:id/force-release`: Force release viewers on a tuner.
- `GET /ws?session_id=XYZ`: WebSocket endpoint for WebRTC signaling and real-time updates.
- `GET /discover.json` & `/lineup.json`: HDHomeRun emulation endpoints.
- `GET /playlist.m3u`: Live M3U channel playlist.
- `GET /epg.xml`: XMLTV formatted program guide.

---

## 📄 License & Credits

- Created and maintained by [Vasilis Koulis (bkbilly)](https://github.com/bkbilly).
- Developed in collaboration with LLMs.
- Licensed under the [MIT License](LICENSE).
