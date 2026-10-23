// DVB-Web Application Frontend Client

let currentSessionID = sessionStorage.getItem('dvb_session_id');
if (!currentSessionID) {
  currentSessionID = 'sess_' + Math.random().toString(36).substring(2, 10);
  sessionStorage.setItem('dvb_session_id', currentSessionID);
}
let channels = [];
let activeChannel = null;
let currentTuners = [];
let currentLanguage = 'eng';
let ws = null;
let pc = null;
let conflictData = null;
let scanDiff = null;

// Determine if a channel has a tuner conflict
function getChannelConflict(channel) {
  if (!channel || !currentTuners || currentTuners.length === 0) return null;

  const enabled = currentTuners.filter(t => t.enabled);
  if (enabled.length === 0) {
    return { reason: 'No tuners enabled', busyTuners: [] };
  }

  // 1. If any enabled tuner is idle, there are enough tuners available!
  const hasIdle = enabled.some(t => t.status === 'idle');
  if (hasIdle) {
    return null;
  }

  // 2. If any active tuner is already tuned to this channel's multiplex frequency: no conflict!
  const activeMatching = enabled.filter(t => t.status === 'active' && t.current_frequency === channel.frequency);
  if (activeMatching.length > 0) {
    return null;
  }

  // 3. If the current user is the ONLY viewer on an active tuner,
  // that tuner belongs to this user and can be switched to any frequency freely without conflict!
  const mySoleTuner = enabled.find(t => 
    t.status === 'active' && 
    t.viewers && 
    t.viewers.length > 0 && 
    t.viewers.every(v => v.id === currentSessionID)
  );
  if (mySoleTuner) {
    return null;
  }

  // 4. Check which tuners are busy with OTHER viewers
  const busyWithOthers = enabled.filter(t => 
    t.status === 'active' && 
    t.viewers && 
    t.viewers.some(v => v.id !== currentSessionID)
  );

  // If all enabled tuners are occupied by other viewers, this channel has a conflict!
  if (busyWithOthers.length === enabled.length) {
    const busyInfo = busyWithOthers
      .map(t => `${t.name}: ${t.current_channel || (t.current_frequency ? (t.current_frequency / 1e6).toFixed(1) + ' MHz' : 'Busy')}`)
      .join(', ');

    return {
      reason: `All tuners busy (${busyInfo})`,
      busyTuners: busyWithOthers
    };
  }

  return null;
}

// EPG Timeline Settings
const PIXELS_PER_MINUTE = 4; // 240px per hour
let timelineStartTime = null;
let timelineEndTime = null;

const video = document.getElementById('tv-video');
const standbyOverlay = document.getElementById('standby-overlay');
const videoOsd = document.getElementById('video-osd');
const zapIndicator = document.getElementById('zap-indicator');
const tunerBadge = document.getElementById('tuner-badge');
const langSelect = document.getElementById('global-lang-select');

// Initialize on Load
document.addEventListener('DOMContentLoaded', async () => {
  setupLanguagePicker();
  setupPlayerControls();
  setupSettingsUI();
  setupKeybindings();
  setupRouting();

  await loadInitialSettings();
  await loadChannels();
  initWebSocket();

  // Video playback listener
  video.addEventListener('playing', () => {
    standbyOverlay.classList.add('hidden');
    const streamLoader = document.getElementById('stream-loader');
    if (streamLoader) {
      streamLoader.style.opacity = '0';
      setTimeout(() => streamLoader.classList.add('hidden'), 250);
    }
    const alertEl = document.getElementById('stream-alert');
    if (alertEl) {
      alertEl.classList.remove('visible');
      alertEl.classList.add('hidden');
    }
  });

  video.addEventListener('ended', () => {
    if (activeChannel) {
      handleStreamStopped('Stream playback ended.', true);
    }
  });

  window.addEventListener('beforeunload', () => {
    stopStream();
  });

  // Handle initial route
  handleRoute();

  // Real-time clock tick for EPG
  setInterval(updateLiveClock, 1000);
  setInterval(updateNowMarkerPosition, 30000);
});

// ==========================================================
// PERSISTENT URL ROUTING (Hash Router: #/tv, #/epg, #/settings)
// ==========================================================
function setupRouting() {
  window.addEventListener('hashchange', handleRoute);
}

function handleRoute() {
  const hash = window.location.hash.replace('#/', '') || 'tv';
  switchTab(hash);
}

function switchTab(tabId) {
  // Update nav buttons
  document.querySelectorAll('.nav-btn').forEach(btn => {
    btn.classList.toggle('active', btn.getAttribute('data-tab') === tabId);
  });

  // Update tab panes
  document.querySelectorAll('.tab-pane').forEach(pane => {
    pane.classList.toggle('active', pane.id === `tab-${tabId}`);
  });

  // Trigger tab-specific loader
  if (tabId === 'epg') {
    loadEPGTimeline(true);
  } else if (tabId === 'settings') {
    refreshTunerStatus();
  } else if (tabId === 'tv') {
    updateMobileVideoHeight();
  }
}

const KNOWN_LANGUAGES = {
  gre: { name: 'Ελληνικά', flag: '🇬🇷' },
  ell: { name: 'Ελληνικά', flag: '🇬🇷' },
  eng: { name: 'English', flag: '🇬🇧' },
  fra: { name: 'Français', flag: '🇫🇷' },
  fre: { name: 'Français', flag: '🇫🇷' },
  deu: { name: 'Deutsch', flag: '🇩🇪' },
  ger: { name: 'Deutsch', flag: '🇩🇪' },
  ita: { name: 'Italiano', flag: '🇮🇹' },
  spa: { name: 'Español', flag: '🇪🇸' },
  tur: { name: 'Türkçe', flag: '🇹🇷' },
  alb: { name: 'Shqip', flag: '🇦🇱' },
  bul: { name: 'Български', flag: '🇧🇬' },
  rus: { name: 'Русский', flag: '🇷🇺' },
};

let availableLanguages = [];

// Multi-language Support
function setupLanguagePicker() {
  langSelect.addEventListener('change', (e) => {
    currentLanguage = e.target.value;
    try {
      localStorage.setItem('dvb_epg_language', currentLanguage);
    } catch (_) {}
    renderChannelList();
    if (activeChannel) {
      updateOSD(activeChannel);
    }
    const epgTab = document.getElementById('tab-epg');
    if (epgTab && epgTab.classList.contains('active')) {
      loadEPGTimeline(false);
    }
  });
}

async function updateAvailableLanguages(languages) {
  if (!languages || languages.length === 0) {
    try {
      const res = await fetch('/api/epg/languages');
      languages = await res.json() || [];
    } catch (e) {
      languages = [];
    }
  }

  // Deduplicate and filter ONLY to broadcasted languages that are known and supported
  const valid = [];
  const seen = new Set();
  languages.forEach(l => {
    let norm = (l || '').toLowerCase().trim();
    if (norm === 'ell') norm = 'gre';
    if (norm && norm.length === 3 && KNOWN_LANGUAGES[norm] && !seen.has(norm)) {
      seen.add(norm);
      valid.push(norm);
    }
  });

  availableLanguages = valid;

  // Clear and populate dropdown with ONLY broadcasted languages
  langSelect.innerHTML = '';
  if (valid.length === 0) {
    const opt = document.createElement('option');
    opt.value = currentLanguage;
    opt.textContent = KNOWN_LANGUAGES[currentLanguage] ? 
      `${KNOWN_LANGUAGES[currentLanguage].flag} ${KNOWN_LANGUAGES[currentLanguage].name}` : 
      currentLanguage.toUpperCase();
    langSelect.appendChild(opt);
    return;
  }

  valid.forEach(code => {
    const info = KNOWN_LANGUAGES[code];
    const label = info ? `${info.flag} ${info.name}` : code.toUpperCase();
    const opt = document.createElement('option');
    opt.value = code;
    opt.textContent = label;
    langSelect.appendChild(opt);
  });

  // Restore cached language or fallback to Greek or first available
  const cachedLang = localStorage.getItem('dvb_epg_language');
  if (cachedLang && valid.includes(cachedLang)) {
    currentLanguage = cachedLang;
  } else if (valid.includes(currentLanguage)) {
    // Keep current
  } else if (valid.includes('gre')) {
    currentLanguage = 'gre';
  } else {
    currentLanguage = valid[0];
  }
  langSelect.value = currentLanguage;
}

function getLocalized(textMap, preferredLang) {
  if (!textMap || Object.keys(textMap).length === 0) return '';
  if (textMap[preferredLang]) return textMap[preferredLang];
  if (preferredLang === 'gre' && textMap['ell']) return textMap['ell'];
  if (preferredLang === 'ell' && textMap['gre']) return textMap['gre'];
  if (textMap['eng']) return textMap['eng'];
  return Object.values(textMap)[0] || '';
}

// Fetch channels from backend
async function loadChannels() {
  try {
    const res = await fetch('/api/channels');
    channels = await res.json() || [];
    renderChannelList();
    renderAdminChannelList();
  } catch (err) {
    console.error('Failed to load channels:', err);
  }
}

// Render Channel Sidebar Cards with keyed in-place updates (no DOM flickering or click dropping)
function renderChannelList(filterText = '', forceReorder = false) {
  const container = document.getElementById('channel-list');
  if (!container) return;

  const filtered = channels.filter(c => 
    c.name.toLowerCase().includes(filterText.toLowerCase()) || 
    c.number.toString().includes(filterText)
  );

  const filteredIds = new Set(filtered.map(c => c.id.toString()));

  // Remove cards no longer in the filtered list
  Array.from(container.children).forEach(child => {
    if (!filteredIds.has(child.dataset.channelId)) {
      child.remove();
    }
  });

  filtered.forEach(c => {
    let card = container.querySelector(`[data-channel-id="${c.id}"]`);
    const isActive = activeChannel && activeChannel.id === c.id;
    const conflict = getChannelConflict(c);
    const nowTitle = c.now_playing ? getLocalized(c.now_playing.titles, currentLanguage) : 'No EPG Info';

    const cardClasses = ['channel-card'];
    if (isActive) cardClasses.push('active');
    if (conflict) cardClasses.push('has-conflict');

    const conflictBadgeHtml = conflict ? 
      `<span class="badge-conflict" title="${conflict.reason}">⚠️ Conflict</span>` : '';
    const cardTitle = conflict ? `${c.name} — ${conflict.reason}` : c.name;

    if (!card) {
      card = document.createElement('div');
      card.dataset.channelId = c.id;
      card.className = cardClasses.join(' ');
      card.title = cardTitle;
      card.onclick = () => zapToChannel(c);
      card.innerHTML = `
        <div class="ch-meta">
          <div class="ch-top-row">
            <span class="badge ch-number-badge">${c.number}</span>
            <span class="ch-name">${c.name}</span>
            ${conflictBadgeHtml}
          </div>
          <div class="ch-now-show">${nowTitle}</div>
        </div>
      `;
      container.appendChild(card);
    } else {
      const targetClass = cardClasses.join(' ');
      if (card.className !== targetClass) {
        card.className = targetClass;
      }
      if (card.title !== cardTitle) {
        card.title = cardTitle;
      }
      if (forceReorder) {
        container.appendChild(card);
      }

      const numBadge = card.querySelector('.ch-number-badge') || card.querySelector('.badge');
      if (numBadge && numBadge.textContent !== String(c.number)) {
        numBadge.textContent = c.number;
      }
      const nameEl = card.querySelector('.ch-name');
      if (nameEl && nameEl.textContent !== c.name) {
        nameEl.textContent = c.name;
      }

      // Remove obsolete logo if still present
      const oldLogo = card.querySelector('.ch-logo-mini');
      if (oldLogo) oldLogo.remove();

      const topRow = card.querySelector('.ch-top-row');
      let badgeConflict = topRow ? topRow.querySelector('.badge-conflict') : null;
      if (conflict) {
        if (!badgeConflict && topRow) {
          const span = document.createElement('span');
          span.className = 'badge-conflict';
          span.title = conflict.reason;
          span.textContent = '⚠️ Conflict';
          topRow.appendChild(span);
        } else if (badgeConflict) {
          badgeConflict.title = conflict.reason;
        }
      } else if (badgeConflict) {
        badgeConflict.remove();
      }

      const titleEl = card.querySelector('.ch-now-show');
      if (titleEl && titleEl.textContent !== nowTitle) {
        titleEl.textContent = nowTitle;
      }
    }
  });
}

// Search Filter
document.getElementById('channel-search').addEventListener('input', (e) => {
  renderChannelList(e.target.value);
});

// WebSocket Connection (Signaling, Channel Zap, Kicks, EPG)
function initWebSocket() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}/ws?session_id=${currentSessionID}`);

  ws.onopen = () => {
    console.log('[WS] Connected to DVB-Web server');
  };

  ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      handleWSMessage(msg);
    } catch (e) {
      console.error('[WS] Parse error:', e);
    }
  };

  ws.onclose = () => {
    console.warn('[WS] Closed, retrying in 2s...');
    setTimeout(initWebSocket, 2000);
  };
}

let currentSignal = null;

function updateSignalDisplay(sig) {
  if (!sig) return;
  currentSignal = sig;
  const badge = document.getElementById('signal-quality-badge');
  const valEl = document.getElementById('sig-quality-val');
  const osdBadge = document.getElementById('osd-signal-badge');
  const osdVal = document.getElementById('osd-signal-val');

  if (sig.has_lock || sig.signal_quality > 0 || sig.signal_strength > 0) {
    if (badge) badge.classList.remove('hidden');
    if (osdBadge) osdBadge.classList.remove('hidden');

    const q = sig.signal_quality || 0;
    const str = sig.signal_strength || 0;
    const qualityText = `${q}%`;

    if (valEl) valEl.textContent = qualityText;
    if (osdVal) osdVal.textContent = qualityText;

    const levelClass = q >= 70 ? 'sig-good' : (q >= 40 ? 'sig-med' : 'sig-poor');
    if (badge) {
      badge.className = `signal-quality-badge ${levelClass}`;
      badge.title = `Signal Quality: ${q}% | Strength: ${str}%${sig.snr_db ? ' | SNR: ' + sig.snr_db.toFixed(1) + ' dB' : ''}${sig.has_lock ? ' (Locked)' : ''}`;
    }
    if (osdBadge) {
      osdBadge.className = `osd-signal-badge ${levelClass}`;
      osdBadge.title = `Signal Quality: ${q}% (Strength: ${str}%)`;
    }
  } else {
    if (badge) badge.classList.add('hidden');
    if (osdBadge) osdBadge.classList.add('hidden');
  }
}

function handleWSMessage(msg) {
  if (msg.error) {
    console.error('[WS Error]', msg.error);
    alert('Tuning Error: ' + msg.error);
    return;
  }
  switch (msg.event) {
    case 'tuned':
      standbyOverlay.classList.add('hidden');
      tunerBadge.className = 'tuner-badge badge-active';
      tunerBadge.textContent = `Tuner ${msg.tuner_id}: Active`;
      activeChannel = msg.channel;
      activeChannel.now_playing = msg.now_playing;
      activeChannel.next_playing = msg.next_playing;
      renderChannelList();
      showOSD(activeChannel);
      if (msg.signal) {
        updateSignalDisplay(msg.signal);
      }
      startStream(activeChannel);
      break;

    case 'signal':
      if (msg.signal) {
        updateSignalDisplay(msg.signal);
      }
      break;

    case 'tuners_update':
      if (msg.tuners) {
        currentTuners = msg.tuners;
        renderChannelList();
        const settingsTab = document.getElementById('tab-settings');
        if (settingsTab && settingsTab.classList.contains('active')) {
          refreshTunerStatus();
        }
      }
      break;

    case 'conflict':
      showConflictModal(msg);
      break;

    case 'kicked':
      handleStreamStopped(`Stream was stopped: ${msg.reason || 'Tuner reclaimed by another user'}`, true);
      activeChannel = null;
      renderChannelList();
      const sigBadge = document.getElementById('signal-quality-badge');
      if (sigBadge) sigBadge.classList.add('hidden');
      break;

    case 'epg_update':
      // Do not wipe or reload the timeline grid while user is watching or browsing!
      // Only silently refresh now/next metadata after a long debounce (60s)
      if (window._epgDebounceTimer) clearTimeout(window._epgDebounceTimer);
      window._epgDebounceTimer = setTimeout(async () => {
        try {
          const res = await fetch('/api/channels');
          const newChans = await res.json() || [];
          if (newChans.length > 0) {
            channels = newChans;
            renderChannelList();
          }
        } catch (_) {}
      }, 60000);
      break;

    case 'scan_progress':
      const scanStatus = document.getElementById('scan-status');
      if (scanStatus) {
        scanStatus.textContent = `Scanning ${(msg.frequency / 1e6).toFixed(1)} MHz (${msg.progress}%)... Discovered: ${msg.found_count} channels`;
      }
      break;

    case 'tuner_released':
      if (conflictData && conflictData.target_channel) {
        zapToChannel(conflictData.target_channel);
        closeConflictModal();
      }
      break;
  }
}

// Unified Single Stream Player (MPEG-TS with synchronized Video & Audio)
let player = null;
let currentStreamingChannelId = null;

function handleStreamStopped(reason = 'Stream was forced to stop', canRetry = true) {
  stopStream();

  const streamLoader = document.getElementById('stream-loader');
  if (streamLoader) {
    streamLoader.classList.add('hidden');
  }

  // Update Standby Card overlay with specific reason
  const standbyIcon = document.getElementById('standby-icon');
  const standbyTitle = document.getElementById('standby-title');
  const standbyDesc = document.getElementById('standby-desc');
  const standbyAction = document.getElementById('btn-standby-action');

  if (standbyIcon) standbyIcon.textContent = '⚠️';
  if (standbyTitle) standbyTitle.textContent = 'Stream Stopped';
  if (standbyDesc) standbyDesc.textContent = reason;
  if (standbyAction) {
    if (canRetry && activeChannel) {
      standbyAction.classList.remove('hidden');
      standbyAction.textContent = `Reconnect to ${activeChannel.name}`;
      standbyAction.onclick = () => {
        standbyAction.classList.add('hidden');
        resetStandbyCard();
        zapToChannel(activeChannel);
      };
    } else {
      standbyAction.classList.add('hidden');
    }
  }

  standbyOverlay.classList.remove('hidden');

  // Also display the prominent floating alert banner over the player
  showStreamAlert(reason);

  // Update tuner status badge
  if (tunerBadge) {
    tunerBadge.className = 'tuner-badge badge-idle';
    tunerBadge.textContent = 'Idle (0% CPU)';
  }
}

function showStreamAlert(message) {
  const alertEl = document.getElementById('stream-alert');
  const msgEl = document.getElementById('stream-alert-msg');
  if (!alertEl || !msgEl) return;

  msgEl.textContent = message;
  alertEl.classList.remove('hidden');
  void alertEl.offsetWidth;
  alertEl.classList.add('visible');

  const btnClose = document.getElementById('btn-close-stream-alert');
  if (btnClose) {
    btnClose.onclick = () => {
      alertEl.classList.remove('visible');
      setTimeout(() => alertEl.classList.add('hidden'), 300);
    };
  }

  if (window._streamAlertTimeout) clearTimeout(window._streamAlertTimeout);
  window._streamAlertTimeout = setTimeout(() => {
    alertEl.classList.remove('visible');
    setTimeout(() => alertEl.classList.add('hidden'), 300);
  }, 10000);
}

function resetStandbyCard() {
  const standbyIcon = document.getElementById('standby-icon');
  const standbyTitle = document.getElementById('standby-title');
  const standbyDesc = document.getElementById('standby-desc');
  const standbyAction = document.getElementById('btn-standby-action');

  if (standbyIcon) standbyIcon.textContent = '💤';
  if (standbyTitle) standbyTitle.textContent = 'Tuner is in Standby (0% CPU)';
  if (standbyDesc) standbyDesc.textContent = 'Select any channel below to wake the tuner and start instant streaming.';
  if (standbyAction) standbyAction.classList.add('hidden');

  const alertEl = document.getElementById('stream-alert');
  if (alertEl) {
    alertEl.classList.remove('visible');
    alertEl.classList.add('hidden');
  }
}

function stopStream() {
  currentStreamingChannelId = null;
  if (player) {
    try {
      player.pause();
      player.unload();
      player.detachMediaElement();
      player.destroy();
    } catch (e) {
      console.warn('[Player] Error destroying player:', e);
    }
    player = null;
  }
}

function startStream(channel) {
  if (!channel) return;
  // If already playing or currently loading this channel, do not restart
  if (player && currentStreamingChannelId === channel.id) {
    return;
  }
  stopStream();
  currentStreamingChannelId = channel.id;
  resetStandbyCard();

  standbyOverlay.classList.add('hidden');
  const streamLoader = document.getElementById('stream-loader');
  const loaderChannelName = document.getElementById('loader-channel-name');
  const loaderChannelDesc = document.getElementById('loader-channel-desc');
  if (streamLoader) {
    if (loaderChannelName) loaderChannelName.textContent = `Tuning ${channel.number}. ${channel.name}...`;
    if (loaderChannelDesc) loaderChannelDesc.textContent = 'Locking DVB frequency & buffering stream';
    streamLoader.classList.remove('hidden');
    streamLoader.style.opacity = '1';
  }

  const streamUrl = `/api/stream?channel_id=${channel.id}&session_id=${currentSessionID}`;

  if (window.mpegts && mpegts.getFeatureList().mseLivePlayback) {
    player = mpegts.createPlayer({
      type: 'mpegts',
      isLive: true,
      url: window.location.origin + streamUrl,
    }, {
      enableWorker: false,
      lazyLoad: false,
      autoCleanupSourceBuffer: true,
      autoCleanupMaxBackwardDuration: 12,
      autoCleanupMinBackwardDuration: 6,
      liveBufferLatencyChasing: true,
      liveSync: true,
    });

    player.attachMediaElement(video);
    player.load();

    player.on(mpegts.Events.ERROR, (errorType, errorDetail, errorInfo) => {
      console.warn('[Player] mpegts error:', errorType, errorDetail, errorInfo);
      if (errorType === mpegts.ErrorTypes.NETWORK_ERROR) {
        handleStreamStopped('Stream connection was closed by the server.', true);
      } else if (errorType === mpegts.ErrorTypes.MEDIA_ERROR) {
        handleStreamStopped('Media decode error in broadcast stream.', true);
      }
    });

    player.on(mpegts.Events.MEDIA_INFO, () => {
      standbyOverlay.classList.add('hidden');
      video.muted = false;
      video.play().catch(e => console.log('[Player] Autoplay error:', e));
    });

    player.play().catch(e => {
      console.log('[Player] Play error:', e);
    });
  } else {
    video.src = streamUrl;
    video.muted = false;
    video.play().catch(e => console.log('[Player] Fallback play error:', e));
  }
}

// Instant Channel Zapping
function zapToChannel(channel) {
  if (!channel) return;

  const conflict = getChannelConflict(channel);
  if (conflict) {
    // There are not enough tuners available!
    // Send tune command over WebSocket to prompt the conflict resolution modal
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        action: 'tune',
        channel_id: channel.id,
      }));
    }
    return;
  }

  activeChannel = channel;

  // Unmute and ensure volume is up
  video.muted = false;
  if (video.volume === 0) video.volume = 1;
  const btnMute = document.getElementById('btn-mute');
  if (btnMute) btnMute.textContent = '🔊';
  const volSlider = document.getElementById('vol-slider');
  if (volSlider && volSlider.value == 0) volSlider.value = 1;

  // Show Quick Zap Number
  zapIndicator.textContent = channel.number;
  zapIndicator.classList.remove('hidden');
  setTimeout(() => zapIndicator.classList.add('hidden'), 1200);

  // Update UI and OSD
  showOSD(channel);
  renderChannelList();

  // Send Zap command over WebSocket
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({
      action: 'tune',
      channel_id: channel.id,
    }));
  }

  // Start unified A/V stream immediately
  startStream(channel);
}

// OSD (On-Screen Display) & Synchronized Player UI
let playerHideTimer = null;
let isMouseOverControls = false;
let isMouseOverOSD = false;

function showPlayerUI(channel) {
  if (playerHideTimer) {
    clearTimeout(playerHideTimer);
    playerHideTimer = null;
  }

  const wrapper = document.getElementById('video-wrapper');
  if (wrapper) {
    wrapper.classList.remove('cursor-hidden');
  }

  const playerControls = document.querySelector('.player-controls');
  if (playerControls) {
    playerControls.classList.remove('controls-autohidden');
  }

  const ch = channel || activeChannel;
  if (ch) {
    updateOSD(ch);
    videoOsd.classList.remove('hidden');
    videoOsd.classList.remove('controls-autohidden');
    videoOsd.style.opacity = '1';
  }

  schedulePlayerUIHide(3500);
}

function schedulePlayerUIHide(delay = 3500) {
  if (playerHideTimer) {
    clearTimeout(playerHideTimer);
    playerHideTimer = null;
  }

  // If mouse is currently hovering over controls or OSD, NEVER hide!
  if (isMouseOverControls || isMouseOverOSD) {
    return;
  }

  playerHideTimer = setTimeout(() => {
    if (isMouseOverControls || isMouseOverOSD) return;

    const playerControls = document.querySelector('.player-controls');
    const wrapper = document.getElementById('video-wrapper');
    if (isControlsOverVideo()) {
      if (playerControls) playerControls.classList.add('controls-autohidden');
      videoOsd.classList.add('controls-autohidden');
      if (wrapper) wrapper.classList.add('cursor-hidden');
    } else {
      // Controls are in the dark letterbox space outside the video:
      // They stay visible, while the OSD above hides
      videoOsd.classList.add('controls-autohidden');
    }
  }, delay);
}

function showOSD(channel) {
  showPlayerUI(channel);
}

function updateOSD(channel) {
  document.getElementById('osd-channel-num').textContent = channel.number;
  document.getElementById('osd-channel-name').textContent = channel.name;

  const nowShow = channel.now_playing;
  if (nowShow) {
    document.getElementById('osd-now-title').textContent = getLocalized(nowShow.titles, currentLanguage);
    const start = new Date(nowShow.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    const end = new Date(nowShow.end_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    document.getElementById('osd-time-start').textContent = start;
    document.getElementById('osd-time-end').textContent = end;

    const total = new Date(nowShow.end_time) - new Date(nowShow.start_time);
    const elapsed = Date.now() - new Date(nowShow.start_time);
    const pct = Math.min(100, Math.max(0, (elapsed / total) * 100));
    document.getElementById('osd-progress-fill').style.width = `${pct}%`;
  } else {
    document.getElementById('osd-now-title').textContent = 'Live Broadcast';
    document.getElementById('osd-time-start').textContent = '--:--';
    document.getElementById('osd-time-end').textContent = '--:--';
    document.getElementById('osd-progress-fill').style.width = '0%';
  }
}

// Conflict Resolution Modal
function showConflictModal(data) {
  conflictData = data;
  const modal = document.getElementById('conflict-modal');
  const details = document.getElementById('conflict-tuner-details');
  details.innerHTML = '';

  data.active_tuners.forEach(t => {
    const viewersHtml = t.viewers && t.viewers.length > 0 ? t.viewers.map(v => 
      `<div class="viewer-row">Viewer IP: <span class="viewer-ip">${v.ip}</span> (watching ${v.channel_name})</div>`
    ).join('') : '<div class="viewer-row">Active multiplex</div>';

    details.innerHTML += `
      <div class="tuner-item">
        <strong>${t.name}</strong> — Channel: ${t.current_channel} (${(t.current_frequency / 1000000).toFixed(1)} MHz)
        <div class="viewers-box">${viewersHtml}</div>
      </div>
    `;
  });

  modal.classList.remove('hidden');
}

function closeConflictModal() {
  document.getElementById('conflict-modal').classList.add('hidden');
  conflictData = null;
}

document.getElementById('btn-cancel-conflict').onclick = closeConflictModal;

document.getElementById('btn-force-kick').onclick = () => {
  if (!conflictData || !conflictData.active_tuners.length) return;
  const targetTuner = conflictData.active_tuners[0];
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({
      action: 'force_release',
      tuner_id: targetTuner.id,
    }));
  }
};

// Aspect Ratio Options
const ASPECT_RATIOS = [
  { id: 'auto', label: 'Auto', className: 'ar-auto', desc: 'Auto (Original Broadcast)' },
  { id: '16-9', label: '16:9', className: 'ar-16-9', desc: '16:9 Widescreen' },
  { id: '4-3', label: '4:3', className: 'ar-4-3', desc: '4:3 Standard' },
  { id: 'fill', label: 'Stretch', className: 'ar-fill', desc: 'Stretch to Window' },
  { id: 'cover', label: 'Zoom', className: 'ar-cover', desc: 'Zoom to Fill' },
];
let currentAspectRatioIndex = 0;

function setupAspectRatioControl() {
  const btn = document.getElementById('btn-aspect-ratio');
  if (!btn) return;

  const savedAR = localStorage.getItem('dvb_aspect_ratio');
  if (savedAR) {
    const idx = ASPECT_RATIOS.findIndex(ar => ar.id === savedAR);
    if (idx !== -1) {
      currentAspectRatioIndex = idx;
    }
  }

  btn.onclick = () => {
    currentAspectRatioIndex = (currentAspectRatioIndex + 1) % ASPECT_RATIOS.length;
    applyAspectRatio(ASPECT_RATIOS[currentAspectRatioIndex]);
    try {
      localStorage.setItem('dvb_aspect_ratio', ASPECT_RATIOS[currentAspectRatioIndex].id);
    } catch (_) {}
    showPlayerUI();
  };
  applyAspectRatio(ASPECT_RATIOS[currentAspectRatioIndex]);
}

// Dynamic mobile video container height so it expands when video can get bigger (e.g. 4:3, tall screens, landscape)
function updateMobileVideoHeight() {
  const wrapper = document.getElementById('video-wrapper');
  if (!wrapper) return;

  if (document.fullscreenElement) {
    wrapper.style.removeProperty('--mobile-video-height');
    return;
  }

  if (window.innerWidth > 860) {
    wrapper.style.removeProperty('--mobile-video-height');
    return;
  }

  let ratio = 16 / 9; // Default widescreen
  const currentAR = (ASPECT_RATIOS && ASPECT_RATIOS[currentAspectRatioIndex]) ? ASPECT_RATIOS[currentAspectRatioIndex].id : 'auto';

  if (currentAR === '4-3') {
    ratio = 4 / 3;
  } else if (currentAR === '16-9') {
    ratio = 16 / 9;
  } else if (video && video.videoWidth > 0 && video.videoHeight > 0) {
    // For auto/fill/cover, use stream native aspect ratio if available
    ratio = video.videoWidth / video.videoHeight;
  }

  const containerWidth = wrapper.clientWidth || window.innerWidth;
  // Calculate ideal height needed for video to expand fully without black pillarbox/letterbox
  const idealHeight = Math.round(containerWidth / ratio);

  // Maximum allowed height: on landscape allow filling height; on portrait allow up to 75vh
  const isLandscape = window.innerWidth > window.innerHeight;
  const maxAvailableHeight = isLandscape
    ? Math.max(window.innerHeight - 50, 200)
    : Math.max(Math.round(window.innerHeight * 0.75), 260);

  const minBaseHeight = Math.min(Math.round(containerWidth * 9 / 16), maxAvailableHeight);
  const targetHeight = Math.min(Math.max(idealHeight, minBaseHeight), maxAvailableHeight);

  wrapper.style.setProperty('--mobile-video-height', `${targetHeight}px`);
}

function applyAspectRatio(ar) {
  const btn = document.getElementById('btn-aspect-ratio');
  video.className = ar.className;
  if (btn) {
    btn.textContent = ar.label;
    btn.title = `Aspect Ratio: ${ar.desc} (Click to cycle)`;
  }
  updateMobileVideoHeight();
}

// Check if player controls bar intersects the actual rendered video content
function isControlsOverVideo() {
  const playerControls = document.querySelector('.player-controls');
  if (!video || !playerControls) return false;
  const standby = document.getElementById('standby-overlay');
  if (standby && !standby.classList.contains('hidden')) return false;

  const videoRect = video.getBoundingClientRect();
  const controlsRect = playerControls.getBoundingClientRect();

  if (videoRect.width === 0 || videoRect.height === 0) return false;

  // Check if controls intersect the visible video frame
  const overlapsY = controlsRect.top < videoRect.bottom - 2 && controlsRect.bottom > videoRect.top + 2;
  const overlapsX = controlsRect.left < videoRect.right && controlsRect.right > videoRect.left;
  return overlapsY && overlapsX;
}

function toggleFullscreen() {
  const wrapper = document.getElementById('video-wrapper');
  if (!document.fullscreenElement) {
    if (wrapper.requestFullscreen) {
      wrapper.requestFullscreen().catch(err => console.log(err));
    }
  } else {
    if (document.exitFullscreen) {
      document.exitFullscreen().catch(err => console.log(err));
    }
  }
}

function setupPlayerControls() {
  setupAspectRatioControl();

  document.getElementById('btn-next-ch').onclick = () => zapDelta(1);
  document.getElementById('btn-prev-ch').onclick = () => zapDelta(-1);

  const volSlider = document.getElementById('vol-slider');
  const btnMute = document.getElementById('btn-mute');

  // Restore volume and mute state from cache
  const savedVol = localStorage.getItem('dvb_player_volume');
  if (savedVol !== null) {
    const vol = parseFloat(savedVol);
    if (!isNaN(vol)) {
      video.volume = vol;
      volSlider.value = vol;
    }
  }
  const savedMuted = localStorage.getItem('dvb_player_muted');
  if (savedMuted !== null) {
    video.muted = (savedMuted === 'true');
    btnMute.textContent = video.muted ? '🔇' : (video.volume == 0 ? '🔇' : '🔊');
  }

  volSlider.oninput = (e) => {
    video.volume = e.target.value;
    video.muted = false;
    btnMute.textContent = video.volume == 0 ? '🔇' : '🔊';
    try {
      localStorage.setItem('dvb_player_volume', video.volume);
      localStorage.setItem('dvb_player_muted', 'false');
    } catch (_) {}
  };

  btnMute.onclick = () => {
    video.muted = !video.muted;
    btnMute.textContent = video.muted ? '🔇' : (video.volume == 0 ? '🔇' : '🔊');
    try {
      localStorage.setItem('dvb_player_muted', video.muted ? 'true' : 'false');
    } catch (_) {}
  };

  // Fullscreen button & double-click video to fullscreen
  document.getElementById('btn-fullscreen').onclick = toggleFullscreen;
  video.addEventListener('dblclick', (e) => {
    e.preventDefault();
    toggleFullscreen();
  });

  // Hover & interaction listeners: do not hide OSD or controls while mouse is over them
  const playerControls = document.querySelector('.player-controls');
  if (playerControls) {
    playerControls.addEventListener('mouseenter', () => {
      isMouseOverControls = true;
      showPlayerUI();
    });
    playerControls.addEventListener('mouseleave', () => {
      isMouseOverControls = false;
      schedulePlayerUIHide(3500);
    });
  }

  videoOsd.addEventListener('mouseenter', () => {
    isMouseOverOSD = true;
    showPlayerUI();
  });
  videoOsd.addEventListener('mouseleave', () => {
    isMouseOverOSD = false;
    schedulePlayerUIHide(3500);
  });

  const wrapper = document.getElementById('video-wrapper');
  wrapper.addEventListener('mousemove', () => {
    showPlayerUI();
  });
  wrapper.addEventListener('mouseleave', () => {
    wrapper.classList.remove('cursor-hidden');
    schedulePlayerUIHide(800);
  });
  window.addEventListener('resize', () => {
    updateMobileVideoHeight();
    // Never show OSD on window resize! Only ensure controls stay visible if in dark letterbox space
    const playerControls = document.querySelector('.player-controls');
    if (!isControlsOverVideo() && playerControls) {
      playerControls.classList.remove('controls-autohidden');
    }
    // Update EPG marker and keep ruler in sync on window resize
    updateNowMarkerPosition();
    const gridScroll = document.getElementById('epg-grid-scroll');
    const rulerScroll = document.getElementById('epg-ruler-scroll');
    if (gridScroll && rulerScroll) {
      rulerScroll.scrollLeft = gridScroll.scrollLeft;
    }
  });
  video.addEventListener('loadedmetadata', () => {
    updateMobileVideoHeight();
    const playerControls = document.querySelector('.player-controls');
    if (!isControlsOverVideo() && playerControls) {
      playerControls.classList.remove('controls-autohidden');
    }
  });
  video.addEventListener('playing', () => {
    updateMobileVideoHeight();
    const playerControls = document.querySelector('.player-controls');
    if (!isControlsOverVideo() && playerControls) {
      playerControls.classList.remove('controls-autohidden');
    }
  });
}

function zapDelta(delta) {
  if (channels.length === 0) return;
  if (!activeChannel) {
    zapToChannel(channels[0]);
    return;
  }
  const idx = channels.findIndex(c => c.id === activeChannel.id);
  const nextIdx = (idx + delta + channels.length) % channels.length;
  zapToChannel(channels[nextIdx]);
}

// Keyboard Navigation (TV Remote simulation)
function setupKeybindings() {
  let digitBuffer = '';
  let digitTimer = null;

  window.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      closeEPGModal();
      closeConflictModal();
      return;
    }
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;

    if (e.key === 'ArrowUp' || e.key === 'PageUp') {
      zapDelta(-1);
    } else if (e.key === 'ArrowDown' || e.key === 'PageDown') {
      zapDelta(1);
    } else if ((e.key === 'f' || e.key === 'F') && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      document.getElementById('btn-fullscreen').click();
    } else if (e.key >= '0' && e.key <= '9') {
      digitBuffer += e.key;
      zapIndicator.textContent = digitBuffer;
      zapIndicator.classList.remove('hidden');

      clearTimeout(digitTimer);
      digitTimer = setTimeout(() => {
        const num = parseInt(digitBuffer, 10);
        const match = channels.find(c => c.number === num);
        if (match) {
          zapToChannel(match);
        }
        digitBuffer = '';
        zapIndicator.classList.add('hidden');
      }, 1000);
    }
  });
}

// ==========================================================
// MODERN TV-STYLE EPG TIMELINE GRID
// ==========================================================
function updateLiveClock() {
  const clockEl = document.getElementById('epg-live-clock');
  if (clockEl) {
    clockEl.textContent = new Date().toLocaleTimeString();
  }
}

async function loadEPGTimeline(isInitial = false) {
  const hoursCount = 168; // 7 days of full TV guide
  if (!timelineStartTime || isInitial) {
    const now = new Date();
    // Timeline starts 2 hours ago, runs for 7 days (168 hours)
    timelineStartTime = new Date(now.getTime() - 2 * 60 * 60 * 1000);
    timelineStartTime.setMinutes(0, 0, 0); // align to hour
  }
  timelineEndTime = new Date(timelineStartTime.getTime() + hoursCount * 60 * 60 * 1000);

  const gridScroll = document.getElementById('epg-grid-scroll');
  const prevScrollLeft = gridScroll ? gridScroll.scrollLeft : 0;
  const prevScrollTop = gridScroll ? gridScroll.scrollTop : 0;

  renderDaySelector(timelineStartTime, hoursCount);
  renderTimeRuler(timelineStartTime, hoursCount);
  renderChannelsColumn();
  await renderProgramRows(timelineStartTime, timelineEndTime);
  updateNowMarkerPosition();
  setupEPGScrolling();

  if (isInitial) {
    setTimeout(jumpToNow, 100);
  } else if (gridScroll) {
    gridScroll.scrollLeft = prevScrollLeft;
    gridScroll.scrollTop = prevScrollTop;
  }
}

function renderDaySelector(startTime, hoursCount) {
  const container = document.getElementById('epg-day-selector');
  if (!container) return;
  container.innerHTML = '';

  const days = [];
  const cur = new Date(startTime);
  cur.setHours(0, 0, 0, 0);

  const endDay = new Date(startTime.getTime() + hoursCount * 60 * 60 * 1000);
  while (cur < endDay) {
    days.push(new Date(cur));
    cur.setDate(cur.getDate() + 1);
  }

  days.slice(0, 7).forEach((day, idx) => {
    const btn = document.createElement('button');
    btn.className = `day-btn ${idx === 0 ? 'active' : ''}`;
    let label = '';
    const nowZero = new Date();
    nowZero.setHours(0, 0, 0, 0);
    const diffDays = Math.round((day - nowZero) / (86400000));
    if (diffDays === 0) {
      label = 'Today';
    } else if (diffDays === 1) {
      label = 'Tomorrow';
    } else {
      label = day.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
    }
    btn.textContent = label;
    btn.onclick = () => {
      document.querySelectorAll('.day-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      btn.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'center' });
      let targetTime = new Date(day);
      targetTime.setHours(8, 0, 0, 0);
      if (targetTime < startTime) targetTime = startTime;
      const minutesSinceStart = (targetTime.getTime() - startTime.getTime()) / (60 * 1000);
      const gridScroll = document.getElementById('epg-grid-scroll');
      gridScroll.scrollTo({ left: Math.max(0, minutesSinceStart * PIXELS_PER_MINUTE), behavior: 'smooth' });
    };
    container.appendChild(btn);
  });
}

function setupEPGScrolling() {
  const gridScroll = document.getElementById('epg-grid-scroll');
  const rulerScroll = document.getElementById('epg-ruler-scroll');
  if (!gridScroll || !rulerScroll) return;

  // Sync horizontal scrolling between timeline ruler and grid body
  gridScroll.onscroll = () => {
    rulerScroll.scrollLeft = gridScroll.scrollLeft;
  };

  // 1. Mouse wheel scrolling
  // Normal vertical wheel scrolls UP and DOWN through channel rows.
  // Shift + Wheel (or dominant horizontal trackpad/wheel) scrolls horizontally across time.
  if (!gridScroll._hasWheelListener) {
    gridScroll._hasWheelListener = true;
    gridScroll.addEventListener('wheel', (e) => {
      if (e.shiftKey) {
        e.preventDefault();
        gridScroll.scrollLeft += e.deltaY;
      } else if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
        gridScroll.scrollLeft += e.deltaX;
      }
      // Natural vertical wheel deltaY scrolls the container vertically (up/down channels)
    }, { passive: false });
  }

  // 2. Click and Drag to pan freely in ALL directions (both horizontally across time and vertically across channels)
  if (!gridScroll._hasDragListener) {
    gridScroll._hasDragListener = true;
    let isDown = false;
    let startX = 0;
    let startY = 0;
    let scrollLeft = 0;
    let scrollTop = 0;
    const DRAG_THRESHOLD = 5;

    gridScroll.addEventListener('mousedown', (e) => {
      // Don't drag if clicking directly on a channel cell in the left column
      if (e.target.closest('.epg-channel-cell')) return;
      isDown = true;
      window._epgIsDragging = false;
      startX = e.pageX;
      startY = e.pageY;
      scrollLeft = gridScroll.scrollLeft;
      scrollTop = gridScroll.scrollTop;
    });

    window.addEventListener('mouseup', () => {
      if (isDown) {
        isDown = false;
        gridScroll.classList.remove('is-dragging');
        setTimeout(() => {
          window._epgIsDragging = false;
        }, 50);
      }
    });

    window.addEventListener('mousemove', (e) => {
      if (!isDown) return;
      const dx = e.pageX - startX;
      const dy = e.pageY - startY;
      if (!window._epgIsDragging && Math.hypot(dx, dy) > DRAG_THRESHOLD) {
        window._epgIsDragging = true;
        gridScroll.classList.add('is-dragging');
      }
      if (window._epgIsDragging) {
        e.preventDefault();
        gridScroll.scrollLeft = scrollLeft - dx;
        gridScroll.scrollTop = scrollTop - dy;
      }
    });
  }

  // 3. Navigation Buttons
  const btnPrev = document.getElementById('btn-epg-prev');
  if (btnPrev) {
    btnPrev.onclick = () => {
      gridScroll.scrollBy({ left: -2 * 60 * PIXELS_PER_MINUTE, behavior: 'smooth' });
    };
  }

  const btnNext = document.getElementById('btn-epg-next');
  if (btnNext) {
    btnNext.onclick = () => {
      gridScroll.scrollBy({ left: 2 * 60 * PIXELS_PER_MINUTE, behavior: 'smooth' });
    };
  }

  const btnNow = document.getElementById('btn-epg-now');
  if (btnNow) {
    btnNow.onclick = jumpToNow;
  }
}

function renderTimeRuler(startTime, hoursCount) {
  const ruler = document.getElementById('epg-time-ruler');
  ruler.innerHTML = '';
  const totalMinutes = hoursCount * 60;
  ruler.style.width = `${totalMinutes * PIXELS_PER_MINUTE}px`;

  for (let i = 0; i < hoursCount; i++) {
    const slotTime = new Date(startTime.getTime() + i * 60 * 60 * 1000);
    const slot = document.createElement('div');
    slot.className = 'ruler-slot';

    if (slotTime.getHours() === 0) {
      const dateBadge = document.createElement('span');
      dateBadge.className = 'ruler-date-badge';
      dateBadge.textContent = slotTime.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
      slot.appendChild(dateBadge);
    }

    const timeText = document.createTextNode(slotTime.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }));
    slot.appendChild(timeText);
    ruler.appendChild(slot);
  }
}

function renderChannelsColumn() {
  const col = document.getElementById('epg-channels-column');
  col.innerHTML = '';

  channels.forEach(c => {
    const cell = document.createElement('div');
    const conflict = getChannelConflict(c);
    cell.className = `epg-channel-cell ${conflict ? 'has-conflict' : ''}`;
    cell.title = conflict ? `${c.name} — ${conflict.reason}` : c.name;
    cell.onclick = () => {
      zapToChannel(c);
      if (!conflict) {
        window.location.hash = '#/tv';
      }
    };

    const conflictIndicator = conflict ? 
      `<span class="epg-conflict-icon" title="${conflict.reason}">⚠️</span>` : '';

    cell.innerHTML = `
      <div class="epg-ch-cell-info">
        <div class="epg-ch-cell-name" title="${c.number}. ${c.name}"><span class="badge" style="font-size:0.7rem; padding:0.12rem 0.35rem; margin-right:0.35rem;">${c.number}</span>${c.name} ${conflictIndicator}</div>
      </div>
    `;
    col.appendChild(cell);
  });
}

async function renderProgramRows(startTime, endTime) {
  const container = document.getElementById('epg-rows-container');
  container.innerHTML = '';

  const totalMinutes = (endTime - startTime) / (60 * 1000);
  const totalWidthPx = totalMinutes * PIXELS_PER_MINUTE;
  document.getElementById('epg-events-canvas').style.width = `${totalWidthPx}px`;

  for (const c of channels) {
    const row = document.createElement('div');
    row.className = 'epg-channel-row-track';
    row.style.width = `${totalWidthPx}px`;

    try {
      const res = await fetch(`/api/epg?channel_id=${c.id}&from=${startTime.toISOString()}&to=${endTime.toISOString()}`);
      const rawEvents = await res.json() || [];

      // Parse, filter valid ranges, and sort strictly chronologically by start_time
      const validEvents = rawEvents
        .map(e => ({
          ...e,
          _start: new Date(e.start_time),
          _end: new Date(e.end_time)
        }))
        .filter(e => !isNaN(e._start) && !isNaN(e._end) && e._end > startTime && e._start < endTime && e._end > e._start)
        .sort((a, b) => a._start - b._start);

      // Track right boundary so events never render on top of each other
      let lastEndPx = 0;

      validEvents.forEach(e => {
        const effectiveStart = e._start < startTime ? startTime : e._start;
        const offsetMinutes = Math.max(0, (effectiveStart - startTime) / (60 * 1000));
        const durationMinutes = Math.max(10, (e._end - effectiveStart) / (60 * 1000));

        let leftPx = offsetMinutes * PIXELS_PER_MINUTE;
        const targetEndPx = (offsetMinutes + durationMinutes) * PIXELS_PER_MINUTE;

        // If this event starts before the previous event ends, push it forward to prevent stacking
        if (leftPx < lastEndPx) {
          leftPx = lastEndPx;
        }

        const widthPx = Math.max(14, targetEndPx - leftPx - 4); // 4px margin
        lastEndPx = leftPx + widthPx + 4;

        const block = document.createElement('div');
        const isNow = Date.now() >= e._start.getTime() && Date.now() < e._end.getTime();
        block.className = `epg-event-block ${isNow ? 'active-now' : ''}`;
        block.style.left = `${leftPx}px`;
        block.style.width = `${widthPx}px`;

        const title = getLocalized(e.titles, currentLanguage) || 'Broadcast';
        const startStr = e._start.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        const endStr = e._end.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

        block.title = `${title} (${startStr} - ${endStr})`;

        block.innerHTML = `
          <div class="epg-block-title">${title}</div>
          <div class="epg-block-time">${startStr} - ${endStr}</div>
        `;
        block.onclick = (ev) => {
          ev.stopPropagation();
          if (window._epgIsDragging) return;
          showEPGModal(e, c.name);
        };

        row.appendChild(block);
      });
    } catch (err) {
      console.error('Failed to load EPG for channel', c.id, err);
    }

    container.appendChild(row);
  }

  // Ensure NOW marker line height covers all channel rows
  updateNowMarkerPosition();
}

// Live Red NOW Line Marker
function updateNowMarkerPosition() {
  if (!timelineStartTime) return;
  const marker = document.getElementById('epg-now-marker');
  if (!marker) return;

  const now = Date.now();
  const minutesSinceStart = (now - timelineStartTime.getTime()) / (60 * 1000);

  if (minutesSinceStart < 0) {
    marker.style.display = 'none';
    return;
  }
  marker.style.display = 'block';
  marker.style.left = `${minutesSinceStart * PIXELS_PER_MINUTE}px`;

  // Stretch red line down to the full height of all channel rows
  const rowsContainer = document.getElementById('epg-rows-container');
  if (rowsContainer && rowsContainer.offsetHeight > 0) {
    marker.style.height = `${rowsContainer.offsetHeight}px`;
  }
}

function jumpToNow() {
  if (!timelineStartTime) return;
  const gridScroll = document.getElementById('epg-grid-scroll');
  const now = Date.now();
  const minutesSinceStart = (now - timelineStartTime.getTime()) / (60 * 1000);
  const targetPx = minutesSinceStart * PIXELS_PER_MINUTE - (gridScroll.clientWidth / 2);
  gridScroll.scrollTo({ left: Math.max(0, targetPx), behavior: 'smooth' });

  // Update active day button
  const dayButtons = document.querySelectorAll('.day-btn');
  if (dayButtons.length > 0) {
    dayButtons.forEach((b, idx) => {
      b.classList.toggle('active', idx === 0);
      if (idx === 0) {
        b.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'center' });
      }
    });
  }
}

// Show EPG Event Details Modal
window.showEPGModal = function(event, channelName) {
  const modal = document.getElementById('epg-modal');
  document.getElementById('modal-event-title').textContent = getLocalized(event.titles, currentLanguage) || 'Program Details';
  document.getElementById('modal-event-channel').textContent = channelName;

  const start = new Date(event.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const end = new Date(event.end_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  document.getElementById('modal-event-time').textContent = `${start} - ${end}`;
  document.getElementById('modal-event-desc').textContent = getLocalized(event.descriptions, currentLanguage) || 'No synopsis provided.';

  modal.classList.remove('hidden');

  // Always reset scroll to the top of the description
  const modalBody = modal.querySelector('.modal-body');
  if (modalBody) modalBody.scrollTop = 0;
};

function closeEPGModal() {
  const modal = document.getElementById('epg-modal');
  if (modal) modal.classList.add('hidden');
}

document.getElementById('btn-close-epg-modal').onclick = closeEPGModal;

// Close modal when clicking outside on backdrop overlay
document.getElementById('epg-modal').onclick = (e) => {
  if (e.target.id === 'epg-modal') {
    closeEPGModal();
  }
};

// ==========================================================
// SETTINGS & TUNER ADMIN
// ==========================================================
async function loadInitialSettings() {
  try {
    const res = await fetch('/api/status');
    const statusData = await res.json();
    if (statusData.tuners) {
      currentTuners = statusData.tuners;
      renderChannelList();
    }
    if (statusData.epg_languages && statusData.epg_languages.length > 0) {
      await updateAvailableLanguages(statusData.epg_languages);
    } else {
      await updateAvailableLanguages();
    }
  } catch (e) {
    await updateAvailableLanguages();
  }

  try {
    const res = await fetch('/api/settings');
    const cfg = await res.json();
    if (cfg.preferred_language && availableLanguages.includes(cfg.preferred_language)) {
      currentLanguage = cfg.preferred_language;
      langSelect.value = currentLanguage;
    }
    if (cfg.transcoder_mode) {
      document.getElementById('cfg-transcoder-mode').value = cfg.transcoder_mode;
    }
    if (cfg.video_bitrate) {
      document.getElementById('cfg-video-bitrate').value = cfg.video_bitrate;
    }
  } catch (e) {}
}

async function refreshTunerStatus() {
  try {
    const res = await fetch('/api/status');
    const data = await res.json();
    if (data.tuners) {
      currentTuners = data.tuners;
      renderChannelList();
    }

    document.getElementById('cfg-detected-hw').value = data.hardware ? data.hardware.toUpperCase() : 'CPU';

    const container = document.getElementById('tuner-list-container');
    container.innerHTML = '';

    data.tuners.forEach(t => {
      const isChecked = t.enabled ? 'checked' : '';
      const statusBadge = t.status === 'active' ? 'badge-active' : 'badge-idle';

      const viewersHtml = t.viewers && t.viewers.length > 0 ? t.viewers.map(v => `
        <div class="viewer-row">
          <span>IP: <strong class="viewer-ip">${v.ip}</strong> (watching ${v.channel_name})</span>
          <button class="btn btn-danger" style="padding:0.15rem 0.4rem; font-size:0.75rem;" onclick="kickTuner(${t.id})">Force Kick</button>
        </div>
      `).join('') : '<span style="color:var(--text-muted); font-size:0.85rem;">No active viewers (Tuner sleeping)</span>';

      const signalHtml = t.signal && (t.signal.has_lock || t.signal.signal_quality > 0) ? `
        <div class="tuner-signal-meter">
          📶 Signal: <strong>${t.signal.signal_strength}%</strong> | Quality: <strong>${t.signal.signal_quality}%</strong>${t.signal.snr_db ? ' (SNR: ' + t.signal.snr_db.toFixed(1) + ' dB)' : ''}${t.signal.has_lock ? ' <span class="badge" style="background:var(--success); font-size:0.7rem; padding:1px 4px;">Locked</span>' : ''}
        </div>
      ` : '';

      container.innerHTML += `
        <div class="tuner-item">
          <div class="tuner-item-top">
            <div class="tuner-left-info">
              <div>
                <span class="tuner-name">${t.name}</span>
                <div class="tuner-sub">${t.frontend_path || 'Virtual Adapter'}</div>
                ${signalHtml}
              </div>
            </div>
            <div class="tuner-actions">
              <span class="tuner-badge ${statusBadge}">${t.status.toUpperCase()}</span>
              <label class="switch" title="Enable/Disable Tuner">
                <input type="checkbox" ${isChecked} onchange="toggleTuner(${t.id}, this.checked)">
                <span class="slider"></span>
              </label>
            </div>
          </div>
          <div class="viewers-box">${viewersHtml}</div>
        </div>
      `;
    });
  } catch (e) {
    console.error('Failed to fetch tuner status:', e);
  }
}

document.getElementById('btn-refresh-tuners').onclick = refreshTunerStatus;

window.toggleTuner = async function(tunerId, enabled) {
  try {
    await fetch(`/api/tuners/${tunerId}/toggle`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: enabled })
    });
  } catch (err) {
    console.error('Failed to toggle tuner', err);
  }
};

window.kickTuner = async function(tunerId) {
  if (!confirm(`Are you sure you want to force-kick all viewers on Tuner ${tunerId}?`)) return;
  await fetch(`/api/tuners/${tunerId}/force-release`, { method: 'POST' });
  refreshTunerStatus();
};

// Channel Scanner
document.getElementById('btn-start-scan').onclick = async () => {
  const status = document.getElementById('scan-status');
  status.textContent = 'Scanning frequencies... please wait...';
  
  try {
    const res = await fetch('/api/scan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ adapter_id: 0 })
    });
    scanDiff = await res.json();
    if (!res.ok) {
      status.textContent = 'Scan failed: ' + (scanDiff.error || 'Server error');
      return;
    }
    status.textContent = 'Scan complete!';

    const resultsBox = document.getElementById('scan-results-box');
    resultsBox.classList.remove('hidden');

    const discovered = scanDiff.discovered_count || 0;
    const missing = (scanDiff.missing_channels && scanDiff.missing_channels.length) || 0;
    document.getElementById('stat-discovered').textContent = `${discovered} Discovered`;
    document.getElementById('stat-missing').textContent = `${missing} Missing`;

    const missingSection = document.getElementById('missing-channels-section');
    const missingList = document.getElementById('missing-channels-list');
    missingList.innerHTML = '';

    if (scanDiff.missing_channels && scanDiff.missing_channels.length > 0) {
      missingSection.classList.remove('hidden');
      scanDiff.missing_channels.forEach(c => {
        missingList.innerHTML += `<span class="badge" style="background-color:var(--danger);">${c.number}: ${c.name}</span>`;
      });
    } else {
      missingSection.classList.add('hidden');
    }

    await loadChannels();
  } catch (err) {
    status.textContent = 'Scan failed: ' + err.message;
  }
};

// Prune Missing Channels
document.getElementById('btn-prune-missing').onclick = async () => {
  if (!scanDiff || !scanDiff.missing_channels.length) return;
  const ids = scanDiff.missing_channels.map(c => c.id);

  if (!confirm(`Are you sure you want to delete ${ids.length} missing channels?`)) return;

  await fetch('/api/scan/prune', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ channel_ids: ids })
  });

  document.getElementById('missing-channels-section').classList.add('hidden');
  document.getElementById('stat-missing').textContent = '0 Missing';
  await loadChannels();
};

// Channel Manager & Drag-and-Drop Reordering
function renderAdminChannelList() {
  const container = document.getElementById('channel-admin-table');
  if (!container) return;
  container.innerHTML = '';

  channels.forEach((c, index) => {
    const row = document.createElement('div');
    row.className = 'admin-channel-row';
    row.draggable = true;
    row.dataset.channelId = c.id;
    row.dataset.index = index;

    row.innerHTML = `
      <div class="admin-ch-left">
        <span class="drag-handle" title="Drag to reorder">☰</span>
        <span class="badge ch-badge">${c.number}</span>
        <div class="admin-ch-info">
          <strong class="admin-ch-name">${c.name}</strong>
          <div class="admin-ch-sub">${(c.frequency / 1000000).toFixed(1)} MHz (SID: ${c.service_id})</div>
        </div>
      </div>
      <div class="admin-ch-actions">
        <button class="btn btn-danger btn-sm" onclick="deleteChannel(${c.id})">Delete</button>
      </div>
    `;

    // HTML5 Drag and Drop listeners
    row.addEventListener('dragstart', (e) => {
      row.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', c.id.toString());
    });

    row.addEventListener('dragend', () => {
      row.classList.remove('dragging');
      container.querySelectorAll('.admin-channel-row').forEach(r => r.classList.remove('drag-over-above', 'drag-over-below'));
    });

    row.addEventListener('dragover', (e) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      const draggingEl = container.querySelector('.dragging');
      if (!draggingEl || draggingEl === row) return;

      const rect = row.getBoundingClientRect();
      const midY = rect.top + rect.height / 2;
      if (e.clientY < midY) {
        row.classList.add('drag-over-above');
        row.classList.remove('drag-over-below');
      } else {
        row.classList.add('drag-over-below');
        row.classList.remove('drag-over-above');
      }
    });

    row.addEventListener('dragleave', () => {
      row.classList.remove('drag-over-above', 'drag-over-below');
    });

    row.addEventListener('drop', async (e) => {
      e.preventDefault();
      row.classList.remove('drag-over-above', 'drag-over-below');
      const draggingEl = container.querySelector('.dragging');
      if (!draggingEl || draggingEl === row) return;

      const rect = row.getBoundingClientRect();
      const midY = rect.top + rect.height / 2;
      if (e.clientY < midY) {
        container.insertBefore(draggingEl, row);
      } else {
        container.insertBefore(draggingEl, row.nextSibling);
      }

      await applyChannelReorder();
    });

    container.appendChild(row);
  });
}

async function applyChannelReorder() {
  const container = document.getElementById('channel-admin-table');
  if (!container) return;
  const rows = Array.from(container.querySelectorAll('.admin-channel-row'));
  const newIdOrder = rows.map(r => Number(r.dataset.channelId));

  const chanMap = new Map();
  channels.forEach(c => chanMap.set(c.id, c));

  const reordered = [];
  newIdOrder.forEach((id, idx) => {
    const ch = chanMap.get(id);
    if (ch) {
      ch.number = idx + 1;
      reordered.push(ch);
    }
  });

  channels.forEach(c => {
    if (!newIdOrder.includes(c.id)) {
      c.number = reordered.length + 1;
      reordered.push(c);
    }
  });

  channels = reordered;

  // Immediately reflect across tabs without interrupting live video stream
  renderChannelList('', true);
  renderAdminChannelList();
  const epgTab = document.getElementById('tab-epg');
  if (epgTab && timelineStartTime && timelineEndTime) {
    renderChannelsColumn();
    renderProgramRows(timelineStartTime, timelineEndTime);
  }

  try {
    await fetch('/api/channels/reorder', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(newIdOrder)
    });
  } catch (err) {
    console.error('Failed to persist channel reorder:', err);
  }
}

window.uploadLogo = async function(channelId, input) {
  if (!input.files || !input.files[0]) return;
  const formData = new FormData();
  formData.append('logo', input.files[0]);

  try {
    const res = await fetch(`/api/channels/${channelId}/logo`, {
      method: 'POST',
      body: formData,
    });
    if (res.ok) {
      await loadChannels();
      if (activeChannel && activeChannel.id === channelId) {
        showOSD(activeChannel);
      }
    } else {
      alert('Failed to upload logo.');
    }
  } catch (err) {
    console.error('Logo upload error:', err);
  }
};

window.deleteChannel = async function(channelId) {
  if (!confirm('Are you sure you want to delete this channel?')) return;
  await fetch(`/api/channels/${channelId}`, { method: 'DELETE' });
  await loadChannels();
};

// External Players (Jellyfin, Plex, Kodi, VLC) Integration URLs
function setupIntegrationCard() {
  const origin = window.location.origin;
  const hdhrInput = document.getElementById('int-hdhr-url');
  const m3uInput = document.getElementById('int-m3u-url');
  const epgInput = document.getElementById('int-epg-url');
  const m3uDownload = document.getElementById('int-m3u-download');
  const epgDownload = document.getElementById('int-epg-download');

  if (hdhrInput) hdhrInput.value = origin;
  if (m3uInput) m3uInput.value = `${origin}/playlist.m3u`;
  if (epgInput) epgInput.value = `${origin}/epg.xml`;
  if (m3uDownload) m3uDownload.href = `${origin}/playlist.m3u`;
  if (epgDownload) epgDownload.href = `${origin}/epg.xml`;

  document.querySelectorAll('.btn-copy').forEach(btn => {
    btn.onclick = () => {
      const targetId = btn.getAttribute('data-target');
      const input = document.getElementById(targetId);
      if (input) {
        input.select();
        navigator.clipboard.writeText(input.value).then(() => {
          const originalText = btn.textContent;
          btn.textContent = '✅ Copied!';
          setTimeout(() => { btn.textContent = originalText; }, 2000);
        }).catch(() => {
          document.execCommand('copy');
          const originalText = btn.textContent;
          btn.textContent = '✅ Copied!';
          setTimeout(() => { btn.textContent = originalText; }, 2000);
        });
      }
    };
  });
}

// Save Global Settings
function setupSettingsUI() {
  setupIntegrationCard();

  document.getElementById('global-settings-form').onsubmit = async (e) => {
    e.preventDefault();
    const updates = {
      transcoder_mode: document.getElementById('cfg-transcoder-mode').value,
      video_bitrate: document.getElementById('cfg-video-bitrate').value,
    };
    await fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(updates),
    });
    alert('Settings updated successfully!');
  };
}
