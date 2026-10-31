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

function escapeHtml(str) {
  if (str === null || str === undefined) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

// ==========================================================
// TOAST NOTIFICATIONS (Non-intrusive popups)
// ==========================================================
function showToast(msg, type = 'info') {
  let toast = document.getElementById('app-toast');
  const targetParent = document.fullscreenElement || document.body;
  if (!toast) {
    toast = document.createElement('div');
    toast.id = 'app-toast';
  }
  if (toast.parentElement !== targetParent) {
    targetParent.appendChild(toast);
  }

  let borderCol = 'rgba(59, 130, 246, 0.4)';
  let bgCol = 'rgba(15, 23, 42, 0.95)';
  let textCol = '#fff';

  if (type === 'error' || msg.includes('⚠️') || msg.includes('Error') || msg.includes('Could not') || msg.includes('Failed')) {
    borderCol = 'rgba(239, 68, 68, 0.6)';
    bgCol = 'rgba(30, 10, 10, 0.95)';
    textCol = '#fca5a5';
  } else if (type === 'success' || msg.includes('✅') || msg.includes('started') || msg.includes('Scheduled') || msg.includes('Stopped')) {
    borderCol = 'rgba(34, 197, 94, 0.6)';
    bgCol = 'rgba(10, 30, 15, 0.95)';
    textCol = '#86efac';
  }

  const isFs = !!document.fullscreenElement;
  const bottomPos = isFs ? '80px' : '24px';
  const posType = isFs ? 'absolute' : 'fixed';

  toast.style.cssText = `
    position: ${posType};
    bottom: ${bottomPos};
    left: 50%;
    transform: translateX(-50%);
    background: ${bgCol};
    backdrop-filter: blur(8px);
    -webkit-backdrop-filter: blur(8px);
    border: 1px solid ${borderCol};
    color: ${textCol};
    padding: 0.65rem 1.25rem;
    border-radius: 8px;
    font-size: 0.88rem;
    font-weight: 600;
    z-index: 99999;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.6);
    transition: opacity 0.3s ease, transform 0.3s ease;
    pointer-events: none;
    opacity: 1;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  `;

  toast.textContent = msg;
  if (toast._timer) clearTimeout(toast._timer);
  toast._timer = setTimeout(() => {
    toast.style.opacity = '0';
  }, 4000);
}

// Global modal confirmation popup replacing browser confirm()
function showConfirm(message, title = 'Confirm Action', okText = 'Confirm', isDanger = true) {
  return new Promise((resolve) => {
    const modal = document.getElementById('app-confirm-modal');
    const titleEl = document.getElementById('confirm-modal-title');
    const msgEl = document.getElementById('confirm-modal-msg');
    const btnOk = document.getElementById('btn-confirm-ok');
    const btnCancel = document.getElementById('btn-confirm-cancel');
    const btnClose = document.getElementById('btn-confirm-close');

    if (!modal) {
      resolve(true);
      return;
    }

    if (titleEl) titleEl.textContent = title;
    if (msgEl) msgEl.textContent = message;
    if (btnOk) {
      btnOk.textContent = okText;
      btnOk.className = isDanger ? 'btn btn-danger' : 'btn btn-primary';
    }

    const cleanup = (result) => {
      modal.classList.add('hidden');
      btnOk.onclick = null;
      btnCancel.onclick = null;
      btnClose.onclick = null;
      modal.onclick = null;
      resolve(result);
    };

    btnOk.onclick = () => cleanup(true);
    btnCancel.onclick = () => cleanup(false);
    btnClose.onclick = () => cleanup(false);
    modal.onclick = (e) => {
      if (e.target.id === 'app-confirm-modal') cleanup(false);
    };

    modal.classList.remove('hidden');
  });
}

// Redirect any stray browser alert() calls to toast popup
window.alert = function(msg) {
  showToast(String(msg), 'warning');
};

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

  // 3. If the current user is the ONLY viewer on an active tuner WITHOUT an active recording,
  // that tuner belongs to this user and can be switched to any frequency freely without conflict!
  const mySoleTuner = enabled.find(t => 
    t.status === 'active' && 
    !t.active_recording &&
    t.viewers && 
    t.viewers.length > 0 && 
    t.viewers.every(v => v.id === currentSessionID)
  );
  if (mySoleTuner) {
    return null;
  }

  // 4. Check which tuners are busy with OTHER viewers or recordings
  const busyWithOthersOrRec = enabled.filter(t => 
    t.status === 'active' && 
    (t.active_recording || (t.viewers && t.viewers.some(v => v.id !== currentSessionID)))
  );

  // If all enabled tuners are occupied, this channel has a conflict!
  if (busyWithOthersOrRec.length === enabled.length) {
    const recTuners = enabled.filter(t => t.active_recording);
    const hasActiveRec = recTuners.length > 0;
    const busyInfo = busyWithOthersOrRec
      .map(t => {
        if (t.active_recording) {
          return `${t.name}: ⏺ Recording "${t.active_recording.title}" (${t.active_recording.channel_name})`;
        }
        return `${t.name}: ${t.current_channel || (t.current_frequency ? (t.current_frequency / 1e6).toFixed(1) + ' MHz' : 'Busy')}`;
      })
      .join(', ');

    return {
      reason: hasActiveRec ? `All tuners busy with active recording (${busyInfo})` : `All tuners busy (${busyInfo})`,
      hasActiveRecording: hasActiveRec,
      activeRecordings: recTuners.map(t => t.active_recording),
      busyTuners: busyWithOthersOrRec
    };
  }

  return null;
}

// EPG Timeline Settings
const PIXELS_PER_MINUTE = 4; // 240px per hour
let timelineStartTime = null;
let timelineEndTime = null;

let video = document.getElementById('tv-video');
const standbyOverlay = document.getElementById('standby-overlay');
const videoOsd = document.getElementById('video-osd');
const zapIndicator = document.getElementById('zap-indicator');
const tunerBadge = document.getElementById('tuner-badge');
const langSelect = document.getElementById('global-lang-select');

function bindPlayerVideoEvents(v) {
  if (!v || v._eventsBound) return;
  v._eventsBound = true;

  v.addEventListener('playing', () => {
    if (v === video) {
      standbyOverlay.classList.add('hidden');
      updateInstantRecordButton();
      updateTimeshiftUI();
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
      updateMobileVideoHeight();
      const playerControls = document.querySelector('.player-controls');
      if (!isControlsOverVideo() && playerControls) {
        playerControls.classList.remove('controls-autohidden');
      }
    }
  });

  v.addEventListener('ended', () => {
    if (v === video && activeChannel && !isSwitchingTracks && !v._ignoreEnded) {
      handleStreamStopped('Stream playback ended.', true);
    }
  });

  v.addEventListener('dblclick', (e) => {
    e.preventDefault();
    toggleFullscreen();
  });

  v.addEventListener('loadedmetadata', () => {
    if (v === video) {
      updateMobileVideoHeight();
      const playerControls = document.querySelector('.player-controls');
      if (!isControlsOverVideo() && playerControls) {
        playerControls.classList.remove('controls-autohidden');
      }
    }
  });

  v.addEventListener('timeupdate', () => {
    if (v === video) {
      if (handoffBridge && v === handoffBridge.prevVideo && video === handoffBridge.prevVideo) {
        if (v.currentTime >= handoffBridge.prevEnd - 0.25) {
          seekToTimeshiftOffset(handoffBridge.nextLiveSeconds);
          return;
        }
      }
      updateTimeshiftUI();
    }
  });
  v.addEventListener('progress', () => {
    if (v === video) updateTimeshiftUI();
  });
  v.addEventListener('play', () => {
    if (v === video) updateTimeshiftUI();
  });
  v.addEventListener('pause', () => {
    if (v === video) updateTimeshiftUI();
  });
  v.addEventListener('seeking', () => {
    if (v === video) updateTimeshiftUI();
  });
  v.addEventListener('seeked', () => {
    if (v === video) updateTimeshiftUI();
  });
}

// Initialize on Load
document.addEventListener('DOMContentLoaded', async () => {
  bindPlayerVideoEvents(document.getElementById('tv-video'));
  bindPlayerVideoEvents(document.getElementById('tv-video-next'));

  setupLanguagePicker();
  setupPlayerControls();
  setupSettingsUI();
  setupKeybindings();
  setupRouting();
  setupEPGSearch();

  updateInstantRecordButton();
  updateTimeshiftUI();

  await loadInitialSettings();
  await loadChannels();
  initWebSocket();

  window.addEventListener('beforeunload', () => {
    stopStream();
  });

  // Handle initial route
  handleRoute();

  // Real-time clock tick for EPG
  setInterval(updateLiveClock, 1000);
  setInterval(updateNowMarkerPosition, 30000);

  // Real-time progress and now-playing transition check for active player OSD
  setInterval(() => {
    if (activeChannel) {
      updateOSD(activeChannel);
    }
  }, 2000);
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

let lastEPGLoadTime = 0;

function clearChannelFilter() {
  const searchInput = document.getElementById('channel-search');
  if (searchInput && searchInput.value !== '') {
    searchInput.value = '';
  }
  channelFilterType = 'all';
  document.querySelectorAll('.ch-type-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.type === 'all');
  });
  renderChannelList('');
}

function switchTab(tabId) {
  // Clear channel search filter whenever switching tabs so returning shows all channels fresh
  clearChannelFilter();

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
    if (!lastEPGLoadTime || (Date.now() - lastEPGLoadTime > 60 * 1000)) {
      loadEPGTimeline(!lastEPGLoadTime);
    } else {
      updateEPGPlayingHighlight();
      updateNowMarkerPosition();
      updateEPGActiveDay();
    }
  } else if (tabId === 'recordings') {
    loadRecordings();
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

// Unicode search normalization: case-insensitive, tone/accent/diacritic-insensitive, Greek sigma-agnostic
function normalizeSearchText(str) {
  if (!str) return '';
  return String(str)
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '') // remove combining diacritics / tones
    .toLowerCase()
    .replace(/ς/g, 'σ') // normalize Greek final sigma to standard sigma
    .replace(/[^\p{L}\p{N}\s]/gu, ' ') // treat punctuation and symbols as whitespace
    .replace(/\s+/g, ' ')
    .trim();
}

let availableLanguages = [];

// Multi-language Support
function setupLanguagePicker() {
  langSelect.addEventListener('change', async (e) => {
    currentLanguage = e.target.value;
    try {
      localStorage.setItem('dvb_epg_language', currentLanguage);
      // Persist chosen language to server settings so it survives refresh & server sync
      await fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ preferred_language: currentLanguage })
      });
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

let channelFilterType = 'all'; // 'all', 'tv', 'radio'

// Render Channel Sidebar Cards with keyed in-place updates (no DOM flickering or click dropping)
function renderChannelList(filterText = '', forceReorder = false) {
  const container = document.getElementById('channel-list');
  if (!container) return;

  // Only show the category selection if both TV and Radio channels exist
  const filterContainer = document.querySelector('.channel-type-filters');
  const hasTV = channels.some(c => !c.is_radio);
  const hasRadio = channels.some(c => !!c.is_radio);

  if (filterContainer) {
    if (hasTV && hasRadio) {
      filterContainer.classList.remove('hidden');
    } else {
      filterContainer.classList.add('hidden');
      channelFilterType = 'all';
      document.querySelectorAll('.ch-type-btn').forEach(b => {
        b.classList.toggle('active', b.dataset.type === 'all');
      });
    }
  }

  const qNorm = normalizeSearchText(filterText);
  const qTokens = qNorm.split(/\s+/).filter(Boolean);

  const filtered = channels.filter(c => {
    if (qTokens.length > 0) {
      const cNorm = normalizeSearchText(`${c.name} ${c.number}`);
      const matchesSearch = qTokens.every(tok => cNorm.includes(tok));
      if (!matchesSearch) return false;
    }
    if (channelFilterType === 'tv') return !c.is_radio;
    if (channelFilterType === 'radio') return !!c.is_radio;
    return true;
  });

  const filteredIds = new Set(filtered.map(c => c.id.toString()));

  // Remove cards no longer in the filtered list
  Array.from(container.children).forEach(child => {
    if (!filteredIds.has(child.dataset.channelId)) {
      child.remove();
    }
  });

  const activeRecordings = (currentTuners || [])
    .filter(t => t.active_recording)
    .map(t => t.active_recording);
  const isAnyRecording = activeRecordings.length > 0;

  filtered.forEach(c => {
    let card = container.querySelector(`[data-channel-id="${c.id}"]`);
    const isActive = activeChannel && activeChannel.id === c.id;
    const isRadio = !!c.is_radio;
    const conflict = getChannelConflict(c);
    const recTuner = (currentTuners || []).find(t => t.active_recording && t.active_recording.channel_id === c.id);
    const isThisRecording = !!recTuner;

    let nowTitle = c.now_playing ? getLocalized(c.now_playing.titles, currentLanguage) : (isRadio ? 'Digital Radio Stream' : 'No EPG Info');
    if (isThisRecording && recTuner.active_recording.title) {
      nowTitle = `🔴 Recording: ${recTuner.active_recording.title}`;
    }

    const cardClasses = ['channel-card'];
    if (isActive) cardClasses.push('active');
    if (isThisRecording) cardClasses.push('is-recording');
    if (conflict) {
      cardClasses.push('has-conflict');
    } else if (isAnyRecording && !isThisRecording) {
      cardClasses.push('can-switch');
    }

    // Determine status badge
    let statusBadgeHtml = '';
    let cardTitle = c.name;
    if (isThisRecording) {
      statusBadgeHtml = `<span class="badge-rec" title="Recording: ${recTuner.active_recording.title}">🔴 REC</span>`;
      cardTitle = `${c.name} — Recording "${recTuner.active_recording.title}"`;
    } else if (conflict) {
      const isRecConflict = conflict.hasActiveRecording;
      const label = isRecConflict ? '⚠️ Stops Rec' : '⚠️ Conflict';
      statusBadgeHtml = `<span class="badge-conflict" title="${conflict.reason}">${label}</span>`;
      cardTitle = `${c.name} — ${conflict.reason}`;
    } else if (isAnyRecording) {
      statusBadgeHtml = `<span class="badge-watchable" title="Available to watch without stopping recording">🟢 Can Watch</span>`;
      cardTitle = `${c.name} — Available to watch`;
    }

    const radioBadgeHtml = isRadio ? '<span class="badge-radio">📻 Radio</span>' : '';

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
            ${radioBadgeHtml}
            <span class="ch-status-wrap">${statusBadgeHtml}</span>
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

      const topRow = card.querySelector('.ch-top-row');
      if (topRow) {
        let rBadge = topRow.querySelector('.badge-radio');
        if (isRadio && !rBadge) {
          rBadge = document.createElement('span');
          rBadge.className = 'badge-radio';
          rBadge.textContent = '📻 Radio';
          const statusWrap = topRow.querySelector('.ch-status-wrap');
          topRow.insertBefore(rBadge, statusWrap);
        } else if (!isRadio && rBadge) {
          rBadge.remove();
        }
      }

      // Remove obsolete logo if still present
      const oldLogo = card.querySelector('.ch-logo-mini');
      if (oldLogo) oldLogo.remove();

      let statusWrap = card.querySelector('.ch-status-wrap');
      if (!statusWrap) {
        if (topRow) {
          const oldConflict = topRow.querySelector('.badge-conflict');
          if (oldConflict) oldConflict.remove();
          statusWrap = document.createElement('span');
          statusWrap.className = 'ch-status-wrap';
          topRow.appendChild(statusWrap);
        }
      }
      if (statusWrap && statusWrap.innerHTML !== statusBadgeHtml) {
        statusWrap.innerHTML = statusBadgeHtml;
      }

      const titleEl = card.querySelector('.ch-now-show');
      if (titleEl && titleEl.textContent !== nowTitle) {
        titleEl.textContent = nowTitle;
      }
    }
  });
}

// Search Filter & Channel Type Filter Chips
const searchInputEl = document.getElementById('channel-search');
if (searchInputEl) {
  searchInputEl.addEventListener('input', (e) => {
    renderChannelList(e.target.value);
  });
  searchInputEl.addEventListener('search', (e) => {
    renderChannelList(e.target.value);
  });
}

document.querySelectorAll('.ch-type-btn').forEach(btn => {
  btn.onclick = () => {
    document.querySelectorAll('.ch-type-btn').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    channelFilterType = btn.dataset.type;
    const searchInput = document.getElementById('channel-search');
    renderChannelList(searchInput ? searchInput.value : '');
  };
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

  // Real-time update for RF Analyzer overlay HUD if currently displayed
  renderRFAnalyzer(sig);
}

function handleWSMessage(msg) {
  if (msg.error) {
    console.error('[WS Error]', msg.error);
    showToast('⚠️ Tuning Error: ' + msg.error, 'error');
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
      updateEPGPlayingHighlight();
      updateInstantRecordButton();
  updateTimeshiftUI();
      updateTimeshiftUI();
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
        updateHeaderTunerBadge();
        updateInstantRecordButton();
  updateTimeshiftUI();
        const settingsTab = document.getElementById('tab-settings');
        if (settingsTab && settingsTab.classList.contains('active')) {
          refreshTunerStatus();
        }
        loadRecordings();
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
      // Promptly update now/next metadata and refresh TV Guide timeline
      if (window._epgDebounceTimer) clearTimeout(window._epgDebounceTimer);
      window._epgDebounceTimer = setTimeout(async () => {
        try {
          const res = await fetch('/api/channels');
          const newChans = await res.json() || [];
          if (newChans.length > 0) {
            channels = newChans;
            if (activeChannel) {
              const freshActive = channels.find(c => c.id === activeChannel.id);
              if (freshActive) {
                activeChannel.now_playing = freshActive.now_playing;
                activeChannel.next_playing = freshActive.next_playing;
                updateOSD(activeChannel);
              }
            }
            renderChannelList();
          }

          // If TV Guide is currently active, update the timeline program rows seamlessly
          const epgTab = document.getElementById('tab-epg');
          if (epgTab && epgTab.classList.contains('active') && timelineStartTime && timelineEndTime) {
            await renderProgramRows(timelineStartTime, timelineEndTime);
          } else {
            // Mark timeline stale so switching to TV Guide loads fresh events immediately
            lastEPGLoadTime = 0;
          }
        } catch (_) {}
      }, 1000);
      break;

    case 'scan_progress': {
      const status = document.getElementById('scan-status');
      const statusPill = document.getElementById('scan-status-pill');
      if (statusPill) statusPill.classList.remove('hidden');
      if (status) {
        status.textContent = `Scanning ${(msg.frequency / 1e6).toFixed(1)} MHz (${msg.progress}%)`;
      }

      const freqLabel = document.getElementById('scan-freq-label');
      if (freqLabel) {
        freqLabel.textContent = `Scanning ${(msg.frequency / 1e6).toFixed(1)} MHz...`;
      }
      const pctLabel = document.getElementById('scan-pct-label');
      if (pctLabel) {
        pctLabel.textContent = `${msg.progress}%`;
      }
      const progressFill = document.getElementById('scan-progress-fill');
      if (progressFill) {
        progressFill.style.width = `${msg.progress}%`;
      }
      const foundCount = document.getElementById('scan-found-count');
      if (foundCount) {
        foundCount.textContent = `${msg.found_count} discovered`;
      }

      if (msg.recent_channels && msg.recent_channels.length > 0) {
        const lockPill = document.getElementById('scan-active-lock-pill');
        if (lockPill) {
          lockPill.classList.remove('hidden');
          setTimeout(() => { lockPill.classList.add('hidden'); }, 4000);
        }
        const ticker = document.getElementById('scan-live-ticker');
        if (ticker) {
          const ph = ticker.querySelector('.ticker-placeholder');
          if (ph) ph.remove();
          msg.recent_channels.forEach(name => {
            const badge = document.createElement('div');
            badge.className = 'ticker-badge';
            badge.innerHTML = `<span class="t-dot"></span><span>${escapeHtml(name)}</span><small style="color:var(--text-muted); margin-left:2px;">(${(msg.frequency / 1e6).toFixed(1)} MHz)</small>`;
            ticker.appendChild(badge);
          });
          ticker.scrollTop = ticker.scrollHeight;
        }
      }
      break;
    }

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

function updateRadioOverlay(channel) {
  const overlay = document.getElementById('radio-overlay');
  const arWrap = document.getElementById('aspect-dropdown-wrap');
  if (!overlay) return;

  if (channel && channel.is_radio) {
    overlay.classList.remove('hidden');
    const stationName = document.getElementById('radio-station-name');
    const nowPlaying = document.getElementById('radio-now-playing');
    if (stationName) stationName.textContent = channel.name;
    if (nowPlaying) {
      const showTitle = channel.now_playing ? getLocalized(channel.now_playing.titles, currentLanguage) : 'Digital Audio Broadcast';
      nowPlaying.textContent = showTitle;
    }
    if (arWrap) arWrap.classList.add('hidden');
  } else {
    overlay.classList.add('hidden');
    if (arWrap) arWrap.classList.remove('hidden');
  }
}

function handleStreamStopped(reason = 'Stream was forced to stop', canRetry = true) {
  stopStream();
  activeChannel = null;
  updateRadioOverlay(null);
  updateEPGPlayingHighlight();
  renderChannelList();
  updateInstantRecordButton();
  updateTimeshiftUI();
  updateTimeshiftUI();

  // Reset tracks and hide track selector buttons when stream stops
  availableChannelTracks = { audio_tracks: [], subtitle_tracks: [] };
  renderTrackMenus();

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
  updateHeaderTunerBadge();
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
  cleanHandoffBridge();
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
  if (window._nextPlayer) {
    try {
      window._nextPlayer.pause();
      window._nextPlayer.unload();
      window._nextPlayer.detachMediaElement();
      window._nextPlayer.destroy();
    } catch (_) {}
    window._nextPlayer = null;
  }
}

function startStream(channel) {
  if (!channel) return;
  // If already playing or currently loading this channel, do not restart
  if (player && currentStreamingChannelId === channel.id) {
    return;
  }
  isUserTimeshifted = false;
  if (video) video.playbackRate = 1.0;
  stopStream();
  currentStreamingChannelId = channel.id;

  // Reset to primary video stage
  const v1 = document.getElementById('tv-video');
  const v2 = document.getElementById('tv-video-next');
  if (v1 && v2) {
    video = v1;
    v1._ignoreEnded = false;
    v2._ignoreEnded = true;
    v1.classList.remove('video-stage-next');
    v1.classList.add('active-stage');
    v1.style.opacity = '1';
    v2.classList.remove('active-stage');
    v2.classList.add('video-stage-next');
    v2.style.opacity = '0';
  }

  updateEPGPlayingHighlight();
  resetStandbyCard();
  updateRadioOverlay(channel);

  updateInteractiveFeaturesUI(channel);

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

  const streamUrl = `/api/stream?channel_id=${channel.id}&session_id=${currentSessionID}&audio_track=${currentAudioTrack}&sub_track=${currentSubTrack}&sub_offset=${currentSubOffsetMs}`;

  if (window.mpegts && mpegts.getFeatureList().mseLivePlayback) {
    player = mpegts.createPlayer({
      type: 'mpegts',
      isLive: true,
      url: window.location.origin + streamUrl,
    }, {
      enableWorker: false,
      lazyLoad: false,
      stashInitialSize: 64 * 1024,
      autoCleanupSourceBuffer: true,
      autoCleanupMaxBackwardDuration: 120,
      autoCleanupMinBackwardDuration: 60,
      liveBufferLatencyChasing: false,
      liveSync: false,
    });

    player.attachMediaElement(video);
    player.load();

    player.on(mpegts.Events.ERROR, (errorType, errorDetail, errorInfo) => {
      console.warn('[Player] mpegts error:', errorType, errorDetail, errorInfo);
      if (currentSubTrack >= 0) {
        showToast('⚠️ Subtitles not available in this broadcast; switching to live stream', 'warning');
        currentSubTrack = -1;
        renderTrackMenus();
        switchStreamTracks(channel);
        return;
      }
      if (errorType === mpegts.ErrorTypes.NETWORK_ERROR) {
        handleStreamStopped('Stream connection was closed by the server.', true);
      } else if (errorType === mpegts.ErrorTypes.MEDIA_ERROR) {
        handleStreamStopped('Media decode error in broadcast stream.', true);
      }
    });

    player.on(mpegts.Events.MEDIA_INFO, () => {
      standbyOverlay.classList.add('hidden');
      updateHeaderTunerBadge();
      video.muted = false;
      video.play().catch(e => console.log('[Player] Autoplay error:', e));
    });

    player.play().catch(e => {
      console.log('[Player] Play error:', e);
    });

    if (currentSubTrack >= 0) {
      setTimeout(() => {
        if (player && currentSubTrack >= 0 && video.paused && (!video.buffered || video.buffered.length === 0)) {
          showToast('⚠️ Subtitle stream timed out; switching to live stream', 'warning');
          currentSubTrack = -1;
          renderTrackMenus();
          switchStreamTracks(channel);
        }
      }, 8000);
    }
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
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        action: 'tune',
        channel_id: channel.id,
      }));
    } else {
      showConflictModal({
        message: conflict.reason,
        has_active_recording: conflict.hasActiveRecording,
        active_recordings: conflict.activeRecordings,
        active_tuners: conflict.busyTuners,
        target_channel: channel
      });
    }
    return;
  }

  if (activeChannel && activeChannel.id !== channel.id) {
    currentAudioTrack = 0;
    currentSubTrack = -1;
    availableChannelTracks = { audio_tracks: [], subtitle_tracks: [] };
    renderTrackMenus();
  }
  activeChannel = channel;
  updateEPGPlayingHighlight();
  renderChannelList();
  fetchAndRenderChannelTracks(channel.id);
  updateInstantRecordButton();
  updateTimeshiftUI();

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

function isOverlayOrModalOpen() {
  const ids = ['shortcuts-modal', 'hbbtv-modal', 'teletext-overlay', 'rf-analyzer-overlay', 'epg-modal', 'conflict-modal'];
  return ids.some(id => {
    const el = document.getElementById(id);
    return el && !el.classList.contains('hidden');
  });
}

function updateInteractiveFeaturesUI(channel) {
  const ch = channel || activeChannel;
  if (!ch) return;

  const hasTxt = !!(availableChannelTracks && availableChannelTracks.has_teletext);
  const hbbtv = (availableChannelTracks && availableChannelTracks.hbbtv_url) || (ch && ch.hbbtv_url) || '';

  const btnHbbtv = document.getElementById('btn-hbbtv-ctrl');
  const btnTxt = document.getElementById('btn-teletext-ctrl');

  if (btnHbbtv) btnHbbtv.classList.toggle('hidden', !hbbtv);
  if (btnTxt) btnTxt.classList.toggle('hidden', !hasTxt);

  if (!hasTxt) {
    const teletextOverlay = document.getElementById('teletext-overlay');
    if (teletextOverlay && !teletextOverlay.classList.contains('hidden')) {
      toggleTeletext(false);
    }
  }
}

const PLAYER_AUTOHIDE_DELAY = 1800;

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
  const timeshiftBar = document.getElementById('timeshift-bar');
  const bottomBar = document.getElementById('player-bottom-bar');
  if (bottomBar) {
    bottomBar.classList.remove('controls-autohidden');
  }
  if (playerControls) {
    playerControls.classList.remove('controls-autohidden');
  }
  if (timeshiftBar) {
    timeshiftBar.classList.remove('controls-autohidden');
  }

  const ch = channel || activeChannel;
  if (ch) {
    updateOSD(ch);
    videoOsd.classList.remove('hidden');
    videoOsd.classList.remove('controls-autohidden');
    videoOsd.style.opacity = '1';
  }

  schedulePlayerUIHide(PLAYER_AUTOHIDE_DELAY);
}

function schedulePlayerUIHide(delay = PLAYER_AUTOHIDE_DELAY) {
  if (playerHideTimer) {
    clearTimeout(playerHideTimer);
    playerHideTimer = null;
  }

  // If mouse is currently hovering over controls or OSD, or any overlay/modal is open, NEVER hide!
  if (isMouseOverControls || isMouseOverOSD || isOverlayOrModalOpen()) {
    const wrapper = document.getElementById('video-wrapper');
    if (wrapper) wrapper.classList.remove('cursor-hidden');
    return;
  }

  playerHideTimer = setTimeout(() => {
    if (isMouseOverControls || isMouseOverOSD || isOverlayOrModalOpen()) return;

    const playerControls = document.querySelector('.player-controls');
    const timeshiftBar = document.getElementById('timeshift-bar');
    const bottomBar = document.getElementById('player-bottom-bar');
    const wrapper = document.getElementById('video-wrapper');
    if (isControlsOverVideo()) {
      if (bottomBar) bottomBar.classList.add('controls-autohidden');
      if (playerControls) playerControls.classList.add('controls-autohidden');
      if (timeshiftBar) timeshiftBar.classList.add('controls-autohidden');
      if (wrapper) wrapper.classList.add('cursor-hidden');
    } else {
      if (bottomBar) bottomBar.classList.remove('controls-autohidden');
      if (playerControls) playerControls.classList.remove('controls-autohidden');
      if (timeshiftBar) timeshiftBar.classList.remove('controls-autohidden');
      if (wrapper) wrapper.classList.remove('cursor-hidden');
    }

    if (isOSDOverVideo()) {
      videoOsd.classList.add('controls-autohidden');
    } else {
      videoOsd.classList.remove('controls-autohidden');
    }
  }, delay);
}

function showOSD(channel) {
  showPlayerUI(channel);
}

let isRefreshingChannelEPG = false;
let lastChannelEPGRefreshTime = 0;

async function refreshActiveChannelEPG(channelId) {
  const now = Date.now();
  if (isRefreshingChannelEPG || (now - lastChannelEPGRefreshTime < 4000)) return;
  isRefreshingChannelEPG = true;
  lastChannelEPGRefreshTime = now;

  try {
    const res = await fetch(`/api/channels/${channelId}`);
    if (res.ok) {
      const updatedChannel = await res.json();
      if (activeChannel && activeChannel.id === channelId) {
        activeChannel.now_playing = updatedChannel.now_playing;
        activeChannel.next_playing = updatedChannel.next_playing;
        updateOSD(activeChannel);
        renderChannelList();
        updateRadioOverlay(activeChannel);
      }
      const idx = channels.findIndex(c => c.id === channelId);
      if (idx !== -1) {
        channels[idx].now_playing = updatedChannel.now_playing;
        channels[idx].next_playing = updatedChannel.next_playing;
      }
    }
  } catch (err) {
    console.warn('[EPG] Error refreshing active channel EPG:', err);
  } finally {
    isRefreshingChannelEPG = false;
  }
}

function updateOSD(channel) {
  if (!channel) return;
  const numEl = document.getElementById('osd-channel-num');
  const nameEl = document.getElementById('osd-channel-name');
  if (numEl) numEl.textContent = channel.number;
  if (nameEl) nameEl.textContent = channel.name;

  updateRadioOverlay(channel);
  updateInteractiveFeaturesUI(channel);

  const nowMs = Date.now();
  let nowShow = channel.now_playing;

  // Auto-advance to next program if the current program has finished
  if (nowShow && nowShow.end_time && nowMs >= new Date(nowShow.end_time).getTime()) {
    if (channel.next_playing) {
      channel.now_playing = channel.next_playing;
      channel.next_playing = null;
      nowShow = channel.now_playing;
    } else {
      channel.now_playing = null;
      nowShow = null;
    }
    // Asynchronously fetch fresh Now & Next from the server
    refreshActiveChannelEPG(channel.id);
  }

  const titleEl = document.getElementById('osd-now-title');
  const startEl = document.getElementById('osd-time-start');
  const endEl = document.getElementById('osd-time-end');
  const progressFill = document.getElementById('osd-progress-fill');

  if (nowShow) {
    if (titleEl) titleEl.textContent = getLocalized(nowShow.titles, currentLanguage) || (channel.is_radio ? 'Digital Radio Stream' : 'Live Broadcast');
    const start = new Date(nowShow.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    const end = new Date(nowShow.end_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    if (startEl) startEl.textContent = start;
    if (endEl) endEl.textContent = end;

    const total = new Date(nowShow.end_time) - new Date(nowShow.start_time);
    const elapsed = nowMs - new Date(nowShow.start_time);
    const pct = total > 0 ? Math.min(100, Math.max(0, (elapsed / total) * 100)) : 0;
    if (progressFill) progressFill.style.width = `${pct}%`;
  } else {
    if (titleEl) titleEl.textContent = channel.is_radio ? 'Digital Radio Stream' : 'Live Broadcast';
    if (startEl) startEl.textContent = '--:--';
    if (endEl) endEl.textContent = '--:--';
    if (progressFill) progressFill.style.width = '0%';

    // If channel has no now_playing and hasn't queried recently, try to fetch EPG
    if (nowMs - lastChannelEPGRefreshTime > 20000) {
      refreshActiveChannelEPG(channel.id);
    }
  }

  // Populate Next Program row
  const nextWrapper = document.getElementById('osd-next-wrapper');
  const nextTitleEl = document.getElementById('osd-next-title');
  const nextTimeEl = document.getElementById('osd-next-time');
  const nextShow = channel.next_playing;

  if (nextShow && nextWrapper) {
    const nextTitle = getLocalized(nextShow.titles, currentLanguage) || 'Upcoming Program';
    const nextStart = new Date(nextShow.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    const nextEnd = new Date(nextShow.end_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    if (nextTitleEl) nextTitleEl.textContent = nextTitle;
    if (nextTimeEl) nextTimeEl.textContent = nextStart;
    nextWrapper.title = `Next: ${nextTitle} (${nextStart} - ${nextEnd}) — Click to view description & schedule recording`;
    nextWrapper.classList.remove('hidden');
  } else if (nextWrapper) {
    nextWrapper.classList.add('hidden');
  }
}

async function openNextProgramDetails() {
  const ch = activeChannel;
  if (!ch) return;

  let event = ch.next_playing;
  // If no next_playing or missing description, query EPG events around current/next time
  if (!event || !event.descriptions || Object.keys(event.descriptions).length === 0) {
    try {
      const now = new Date();
      const from = new Date(now.getTime() - 3600000).toISOString();
      const to = new Date(now.getTime() + 12 * 3600000).toISOString();
      const res = await fetch(`/api/epg?channel_id=${ch.id}&from=${from}&to=${to}`);
      if (res.ok) {
        const events = await res.json();
        if (events && events.length > 0) {
          const nowMs = now.getTime();
          const currentEnd = ch.now_playing && ch.now_playing.end_time ? new Date(ch.now_playing.end_time).getTime() : nowMs;
          const nextEv = events.find(e => {
            const s = new Date(e.start_time).getTime();
            return s >= currentEnd - 60000 && s > nowMs;
          });
          if (nextEv) {
            event = nextEv;
            ch.next_playing = nextEv;
            updateOSD(ch);
          }
        }
      }
    } catch (e) {
      console.warn('Could not fetch Next EPG details:', e);
    }
  }

  if (event) {
    window.showEPGModal(event, ch.name);
  } else {
    showToast('No upcoming program information available for this channel.', 'info');
  }
}

async function openLiveProgramDetails() {
  const ch = activeChannel;
  if (!ch) return;

  let event = ch.now_playing;
  // If the stored event has expired, clear it so fresh current event is fetched
  if (event && event.end_time && Date.now() >= new Date(event.end_time).getTime()) {
    event = null;
  }

  // If no now_playing or missing description, query EPG events around current time
  if (!event || !event.descriptions || Object.keys(event.descriptions).length === 0) {
    try {
      const now = new Date();
      const from = new Date(now.getTime() - 2 * 3600000).toISOString();
      const to = new Date(now.getTime() + 2 * 3600000).toISOString();
      const res = await fetch(`/api/epg?channel_id=${ch.id}&from=${from}&to=${to}`);
      if (res.ok) {
        const events = await res.json();
        if (events && events.length > 0) {
          const nowMs = now.getTime();
          const currentEv = events.find(e => {
            const s = new Date(e.start_time).getTime();
            const end = new Date(e.end_time).getTime();
            return nowMs >= s && nowMs < end;
          });
          if (currentEv) {
            event = currentEv;
            ch.now_playing = currentEv;
          }
        }
      }
    } catch (e) {
      console.warn('Could not fetch EPG details:', e);
    }
  }

  if (event) {
    window.showEPGModal(event, ch.name);
  } else {
    window.showEPGModal({
      titles: { [currentLanguage]: ch.name },
      descriptions: { [currentLanguage]: 'No detailed EPG synopsis is currently available for this program.' },
      start_time: new Date().toISOString(),
      end_time: new Date(Date.now() + 3600000).toISOString(),
      channel_id: ch.id
    }, ch.name);
  }
}

// Conflict Resolution Modal
function showConflictModal(data) {
  conflictData = data;
  const modal = document.getElementById('conflict-modal');
  const title = document.getElementById('conflict-title');
  const msg = document.getElementById('conflict-message');
  const details = document.getElementById('conflict-tuner-details');
  const btnStopRec = document.getElementById('btn-stop-rec-tune');
  const btnForceKick = document.getElementById('btn-force-kick');
  details.innerHTML = '';

  const hasRec = data.has_active_recording || (data.active_recordings && data.active_recordings.length > 0);

  if (hasRec) {
    if (title) title.textContent = 'Active Recording Conflict';
    if (msg) msg.textContent = 'All tuners are busy and a broadcast is currently being recorded. Changing channels requires stopping the recording.';
    if (btnStopRec) {
      btnStopRec.classList.remove('hidden');
      const recTitle = data.active_recordings && data.active_recordings[0] ? data.active_recordings[0].title : 'Active Recording';
      btnStopRec.textContent = `Stop "${recTitle}" & Watch Channel`;
    }
    if (btnForceKick) btnForceKick.classList.add('hidden');
  } else {
    if (title) title.textContent = 'Tuner Conflict';
    if (msg) msg.textContent = 'All tuners are currently in use on different frequencies.';
    if (btnStopRec) btnStopRec.classList.add('hidden');
    if (btnForceKick) btnForceKick.classList.remove('hidden');
  }

  if (data.active_tuners) {
    data.active_tuners.forEach(t => {
      let extraHtml = '';
      if (t.active_recording) {
        extraHtml = `<div class="viewer-row" style="color: #f87171; font-weight: 600;">⏺ Recording: "${t.active_recording.title}" on ${t.active_recording.channel_name}</div>`;
      } else if (t.viewers && t.viewers.length > 0) {
        extraHtml = t.viewers.map(v => 
          `<div class="viewer-row">Viewer IP: <span class="viewer-ip">${v.ip}</span> (watching ${v.channel_name})</div>`
        ).join('');
      } else {
        extraHtml = '<div class="viewer-row">Active multiplex</div>';
      }

      details.innerHTML += `
        <div class="tuner-item">
          <strong>${t.name}</strong> — Channel: ${t.current_channel || 'Multiplex'} (${(t.current_frequency / 1000000).toFixed(1)} MHz)
          <div class="viewers-box">${extraHtml}</div>
        </div>
      `;
    });
  }

  modal.classList.remove('hidden');
}

function closeConflictModal() {
  document.getElementById('conflict-modal').classList.add('hidden');
  conflictData = null;
}

document.getElementById('btn-cancel-conflict').onclick = closeConflictModal;

const btnStopRecTune = document.getElementById('btn-stop-rec-tune');
if (btnStopRecTune) {
  btnStopRecTune.onclick = async () => {
    if (!conflictData) return;
    const recs = conflictData.active_recordings || [];
    const targetChannel = conflictData.target_channel || activeChannel;
    if (recs.length > 0) {
      const recId = recs[0].recording_id || recs[0].id;
      try {
        await fetch(`/api/recordings/${recId}/stop-and-tune`, { method: 'POST' });
      } catch (e) {
        console.warn('Error stopping recording:', e);
      }
      if (ws && ws.readyState === WebSocket.OPEN && targetChannel) {
        ws.send(JSON.stringify({
          action: 'stop_recording_and_tune',
          recording_id: recId,
          channel_id: targetChannel.id
        }));
      }
    }
    closeConflictModal();
    if (targetChannel) {
      setTimeout(() => {
        zapToChannel(targetChannel);
      }, 400);
    }
  };
}

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
  const menu = document.getElementById('aspect-ratio-menu');
  if (!btn || !menu) return;

  const savedAR = localStorage.getItem('dvb_aspect_ratio');
  if (savedAR) {
    const idx = ASPECT_RATIOS.findIndex(ar => ar.id === savedAR);
    if (idx !== -1) {
      currentAspectRatioIndex = idx;
    }
  }

  btn.onclick = (e) => {
    e.stopPropagation();
    const audioMenu = document.getElementById('audio-tracks-menu');
    const subMenu = document.getElementById('sub-tracks-menu');
    if (audioMenu) audioMenu.classList.add('hidden');
    if (subMenu) subMenu.classList.add('hidden');
    menu.classList.toggle('hidden');
    showPlayerUI();
  };

  renderAspectRatioMenu();
  applyAspectRatio(ASPECT_RATIOS[currentAspectRatioIndex]);
}

function renderAspectRatioMenu() {
  const menu = document.getElementById('aspect-ratio-menu');
  if (!menu) return;
  menu.innerHTML = '';

  ASPECT_RATIOS.forEach((ar, idx) => {
    const item = document.createElement('button');
    const isActive = idx === currentAspectRatioIndex;
    item.className = `track-item ${isActive ? 'active' : ''}`;
    item.innerHTML = `<span>${ar.label}</span><span class="track-item-tag">${ar.desc}</span>`;
    item.onclick = (e) => {
      e.stopPropagation();
      currentAspectRatioIndex = idx;
      applyAspectRatio(ar);
      try {
        localStorage.setItem('dvb_aspect_ratio', ar.id);
      } catch (_) {}
      menu.classList.add('hidden');
      renderAspectRatioMenu();
      showPlayerUI();
    };
    menu.appendChild(item);
  });
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
  const v1 = document.getElementById('tv-video');
  const v2 = document.getElementById('tv-video-next');
  if (v1) {
    const isNext = v1.classList.contains('video-stage-next');
    v1.className = `tv-video-stage ${ar.className} ${isNext ? 'video-stage-next' : 'active-stage'}`;
  }
  if (v2) {
    const isNext = v2.classList.contains('video-stage-next');
    v2.className = `tv-video-stage ${ar.className} ${isNext ? 'video-stage-next' : 'active-stage'}`;
  }
  if (btn) {
    btn.textContent = ar.label;
    btn.title = `Aspect Ratio: ${ar.desc}`;
  }
  updateMobileVideoHeight();
}

// Check if player controls bar intersects the actual rendered video content
function isControlsOverVideo() {
  const bottomBar = document.getElementById('player-bottom-bar');
  const playerControls = document.querySelector('.player-controls');
  const targetEl = bottomBar || playerControls;
  if (!video || !targetEl) return false;
  const standby = document.getElementById('standby-overlay');
  if (standby && !standby.classList.contains('hidden')) return false;

  const videoRect = video.getBoundingClientRect();
  const controlsRect = targetEl.getBoundingClientRect();

  if (videoRect.width === 0 || videoRect.height === 0) return false;

  // Check if controls intersect the visible video frame
  const overlapsY = controlsRect.top < videoRect.bottom - 2 && controlsRect.bottom > videoRect.top + 2;
  const overlapsX = controlsRect.left < videoRect.right && controlsRect.right > videoRect.left;
  return overlapsY && overlapsX;
}

// Check if video OSD banner intersects the actual rendered video content
function isOSDOverVideo() {
  if (!video || !videoOsd) return false;
  const standby = document.getElementById('standby-overlay');
  if (standby && !standby.classList.contains('hidden')) return false;

  const videoRect = video.getBoundingClientRect();
  const osdRect = videoOsd.getBoundingClientRect();

  if (videoRect.width === 0 || videoRect.height === 0 || osdRect.width === 0 || osdRect.height === 0) return false;

  const overlapsY = osdRect.top < videoRect.bottom - 2 && osdRect.bottom > videoRect.top + 2;
  const overlapsX = osdRect.left < videoRect.right && osdRect.right > videoRect.left;
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

  const updateVolUI = () => {
    const isMuted = video.muted || video.volume === 0;
    btnMute.textContent = isMuted ? '🔇' : (video.volume < 0.5 ? '🔉' : '🔊');
    const volGroup = document.querySelector('.vol-group');
    if (volGroup) {
      volGroup.classList.toggle('is-muted', isMuted);
    }
  };

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
  }
  updateVolUI();

  volSlider.oninput = (e) => {
    video.volume = parseFloat(e.target.value);
    video.muted = false;
    updateVolUI();
    try {
      localStorage.setItem('dvb_player_volume', video.volume);
      localStorage.setItem('dvb_player_muted', 'false');
    } catch (_) {}
  };

  btnMute.onclick = () => {
    if (video.muted) {
      video.muted = false;
      if (video.volume === 0) {
        video.volume = 0.5;
        volSlider.value = 0.5;
      }
    } else {
      video.muted = true;
    }
    updateVolUI();
    try {
      localStorage.setItem('dvb_player_muted', video.muted ? 'true' : 'false');
      localStorage.setItem('dvb_player_volume', video.volume);
    } catch (_) {}
  };

  // Fullscreen button & double-click video to fullscreen
  document.getElementById('btn-fullscreen').onclick = toggleFullscreen;
  video.addEventListener('dblclick', (e) => {
    e.preventDefault();
    toggleFullscreen();
  });

  // Hover & interaction listeners: do not hide OSD or controls while mouse is over them
  const bottomBar = document.getElementById('player-bottom-bar');
  if (bottomBar) {
    bottomBar.addEventListener('mouseenter', () => {
      isMouseOverControls = true;
      showPlayerUI();
    });
    bottomBar.addEventListener('mouseleave', () => {
      isMouseOverControls = false;
      schedulePlayerUIHide(500);
    });
  }

  const playerControls = document.querySelector('.player-controls');
  if (playerControls) {
    playerControls.addEventListener('mouseenter', () => {
      isMouseOverControls = true;
      showPlayerUI();
    });
    playerControls.addEventListener('mouseleave', () => {
      isMouseOverControls = false;
      schedulePlayerUIHide(500);
    });
  }

  const timeshiftBar = document.getElementById('timeshift-bar');
  if (timeshiftBar) {
    timeshiftBar.addEventListener('mouseenter', () => {
      isMouseOverControls = true;
      showPlayerUI();
    });
    timeshiftBar.addEventListener('mouseleave', () => {
      isMouseOverControls = false;
      schedulePlayerUIHide(500);
    });
  }

  initTimeshift();
  setupInteractiveFeatures();

  videoOsd.addEventListener('mouseenter', () => {
    isMouseOverOSD = true;
    showPlayerUI();
  });
  videoOsd.addEventListener('mouseleave', () => {
    isMouseOverOSD = false;
    schedulePlayerUIHide(500);
  });
  videoOsd.addEventListener('click', (e) => {
    if (e.target.closest('#osd-next-wrapper')) {
      openNextProgramDetails();
    } else {
      openLiveProgramDetails();
    }
  });

  const wrapper = document.getElementById('video-wrapper');
  wrapper.addEventListener('mousemove', () => {
    showPlayerUI();
  });
  wrapper.addEventListener('mouseleave', () => {
    wrapper.classList.remove('cursor-hidden');
    schedulePlayerUIHide(500);
  });

  // Scrollwheel on live TV to change volume
  wrapper.addEventListener('wheel', (e) => {
    if (isOverlayOrModalOpen()) return;
    if (e.target.closest('.aspect-ratio-menu') || e.target.closest('.audio-tracks-menu') || e.target.closest('.subtitles-menu')) return;
    e.preventDefault();
    const step = 0.05;
    const delta = e.deltaY < 0 ? step : -step;
    adjustVolume(delta);
    showPlayerUI();
  }, { passive: false });
  window.addEventListener('resize', () => {
    updateMobileVideoHeight();
    // Never show OSD on window resize! Only ensure controls stay visible if in dark letterbox space
    const playerControls = document.querySelector('.player-controls');
    if (!isControlsOverVideo() && playerControls) {
      playerControls.classList.remove('controls-autohidden');
    }
    // Update EPG marker and keep ruler in sync on window resize
    updateNowMarkerPosition();
    updateCachedLayoutMetrics();
    const gridScroll = document.getElementById('epg-grid-scroll');
    const rulerScroll = document.getElementById('epg-ruler-scroll');
    if (gridScroll && rulerScroll) {
      rulerScroll.scrollLeft = gridScroll.scrollLeft;
    }
    updateEPGActiveDay();
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

function adjustVolume(delta) {
  const volSlider = document.getElementById('vol-slider');
  const btnMute = document.getElementById('btn-mute');
  const v1 = document.getElementById('tv-video');
  const v2 = document.getElementById('tv-video-next');
  const currentVol = (video ? video.volume : (v1 ? v1.volume : 1));
  const newVol = Math.max(0, Math.min(1, Math.round((currentVol + delta) * 100) / 100));

  if (video) {
    video.volume = newVol;
    video.muted = false;
  }
  if (v1 && v1 !== video) {
    v1.volume = newVol;
    v1.muted = false;
  }
  if (v2 && v2 !== video) {
    v2.volume = newVol;
    v2.muted = false;
  }

  if (volSlider) volSlider.value = newVol;
  if (btnMute) btnMute.textContent = newVol === 0 ? '🔇' : (newVol < 0.5 ? '🔉' : '🔊');
  const volGroup = document.querySelector('.vol-group');
  if (volGroup) volGroup.classList.toggle('is-muted', newVol === 0);
  try {
    localStorage.setItem('dvb_player_volume', newVol);
    localStorage.setItem('dvb_player_muted', 'false');
  } catch (_) {}
}

function cycleAspectRatio() {
  currentAspectRatioIndex = (currentAspectRatioIndex + 1) % ASPECT_RATIOS.length;
  applyAspectRatio(ASPECT_RATIOS[currentAspectRatioIndex]);
  try {
    localStorage.setItem('dvb_aspect_ratio', ASPECT_RATIOS[currentAspectRatioIndex].id);
  } catch (_) {}
  renderAspectRatioMenu();
  showPlayerUI();
}

// ==========================================================
// TIMESHIFT (PAUSE & REWIND LIVE BROADCAST)
// ==========================================================
let isScrubbingTimeshift = false;
let isUserTimeshifted = false;
let handoffBridge = null;

function cleanHandoffBridge() {
  if (!handoffBridge) return;
  const bridge = handoffBridge;
  handoffBridge = null;
  if (bridge.prevPlayer) {
    try {
      bridge.prevPlayer.pause();
      bridge.prevPlayer.unload();
      bridge.prevPlayer.detachMediaElement();
      bridge.prevPlayer.destroy();
    } catch (_) {}
  }
  if (bridge.prevVideo) {
    try {
      bridge.prevVideo.removeAttribute('src');
      bridge.prevVideo.load();
    } catch (_) {}
  }
}

// Calculate unified timeshift buffer state across both the current live stream and any prior bridged buffer
function getUnifiedTimeshiftState() {
  const activeVid = video;
  if (!activeVid) {
    return { hasBuffer: false, totalDuration: 0, currentOffset: 0, isLive: true };
  }

  // If no bridge active, fallback to standard single-video buffer
  if (!handoffBridge || !handoffBridge.prevVideo) {
    if (!activeVid.buffered || activeVid.buffered.length === 0) {
      return { hasBuffer: false, totalDuration: 0, currentOffset: 0, isLive: true };
    }
    const s = activeVid.buffered.start(0);
    const e = activeVid.buffered.end(activeVid.buffered.length - 1);
    const duration = Math.max(0, e - s);
    const currentOffset = Math.max(0, e - activeVid.currentTime);
    return {
      hasBuffer: duration >= 0.5,
      totalDuration: duration,
      currentOffset: currentOffset,
      isLive: currentOffset < 2.5,
      activeVid: activeVid,
    };
  }

  const { prevVideo, prevStart, prevEnd, prevDuration, nextVideo, nextStart } = handoffBridge;

  // Measure buffer of next (live) video
  const nextHasBuffer = nextVideo && nextVideo.buffered && nextVideo.buffered.length > 0;
  const nextEnd = nextHasBuffer ? nextVideo.buffered.end(nextVideo.buffered.length - 1) : (nextVideo.currentTime || nextStart);
  const nextBufStart = nextHasBuffer ? nextVideo.buffered.start(0) : nextStart;
  const nextDuration = Math.max(0, nextEnd - nextBufStart);

  // If nextVideo has accumulated 120s of its own backward buffer, the old bridge is no longer needed
  if (nextDuration >= 120) {
    cleanHandoffBridge();
    return getUnifiedTimeshiftState();
  }

  // Combined buffer duration (past buffer from prevVideo + live stream buffer from nextVideo)
  const nextLiveSeconds = Math.max(0, nextEnd - nextStart);
  const totalDuration = prevDuration + nextLiveSeconds;

  let currentOffset = 0;
  if (activeVid === nextVideo) {
    currentOffset = Math.max(0, nextEnd - nextVideo.currentTime);
  } else if (activeVid === prevVideo) {
    const secondsBeforeHandoff = Math.max(0, prevEnd - prevVideo.currentTime);
    currentOffset = nextLiveSeconds + secondsBeforeHandoff;
  }

  return {
    hasBuffer: totalDuration >= 0.5,
    totalDuration: totalDuration,
    currentOffset: currentOffset,
    isLive: currentOffset < 2.5 && activeVid === nextVideo,
    isBridged: true,
    nextLiveSeconds: nextLiveSeconds,
    nextEnd: nextEnd,
    prevEnd: prevEnd,
    prevStart: prevStart,
    nextVideo: nextVideo,
    prevVideo: prevVideo,
  };
}

function getTimeshiftBufferRange() {
  const state = getUnifiedTimeshiftState();
  return {
    start: 0,
    end: state.totalDuration,
    duration: state.totalDuration,
  };
}

function formatTimeshiftOffset(seconds) {
  const s = Math.max(0, Math.round(seconds));
  const m = Math.floor(s / 60);
  const remS = s % 60;
  return `-${m}:${remS < 10 ? '0' : ''}${remS}`;
}

function seekToTimeshiftOffset(targetOffset) {
  const state = getUnifiedTimeshiftState();
  if (!state.hasBuffer) return;

  targetOffset = Math.max(0, Math.min(targetOffset, state.totalDuration));
  const isTargetLive = targetOffset < 2.5;

  if (!state.isBridged) {
    // Normal single-video seek
    if (!video || !video.buffered || video.buffered.length === 0) return;
    const e = video.buffered.end(video.buffered.length - 1);
    const s = video.buffered.start(0);
    const targetTime = isTargetLive ? Math.max(s, e - 0.5) : Math.max(s, e - targetOffset);
    isUserTimeshifted = !isTargetLive;
    video.currentTime = targetTime;
    if (isTargetLive && video.paused) {
      video.play().catch(e => console.log('[Timeshift] Resume error:', e));
    }
    updateTimeshiftUI();
    showPlayerUI();
    return;
  }

  // Bridged dual-video seek: route to nextVideo (post-switch) or prevVideo (pre-switch history)
  const { nextLiveSeconds, nextEnd, prevEnd, prevStart, nextVideo, prevVideo } = state;

  if (targetOffset <= nextLiveSeconds) {
    // Target is in nextVideo (live / post-switch stream with current track)
    const nextStartBuf = (nextVideo.buffered && nextVideo.buffered.length > 0) ? nextVideo.buffered.start(0) : 0;
    const targetTime = isTargetLive ? Math.max(nextStartBuf, nextEnd - 0.5) : Math.max(nextStartBuf, nextEnd - targetOffset);

    if (video !== nextVideo) {
      // Swap visual stage to nextVideo
      prevVideo.pause();
      prevVideo.muted = true;
      nextVideo.classList.remove('video-stage-next');
      nextVideo.classList.add('active-stage');
      prevVideo.classList.remove('active-stage');
      prevVideo.classList.add('video-stage-next');
      video = nextVideo;
      player = handoffBridge.nextPlayer;
    }

    nextVideo.muted = false;
    nextVideo.currentTime = targetTime;
    isUserTimeshifted = !isTargetLive;
    if (nextVideo.paused) {
      nextVideo.play().catch(e => console.log('[Timeshift] NextVid resume error:', e));
    }
  } else {
    // Target is in prevVideo (pre-switch stream history)
    const secondsBeforeHandoff = targetOffset - nextLiveSeconds;
    const targetTime = Math.max(prevStart, prevEnd - secondsBeforeHandoff);

    if (video !== prevVideo) {
      // Swap visual stage to prevVideo
      nextVideo.pause();
      nextVideo.muted = true;
      prevVideo.classList.remove('video-stage-next');
      prevVideo.classList.add('active-stage');
      nextVideo.classList.remove('active-stage');
      nextVideo.classList.add('video-stage-next');
      video = prevVideo;
      player = handoffBridge.prevPlayer;
    }

    prevVideo.muted = false;
    prevVideo.currentTime = targetTime;
    isUserTimeshifted = true;
    if (prevVideo.paused) {
      prevVideo.play().catch(e => console.log('[Timeshift] PrevVid play error:', e));
    }
  }

  updateTimeshiftUI();
  showPlayerUI();
}

function updateTimeshiftUI() {
  const bar = document.getElementById('timeshift-bar');
  if (bar) {
    bar.classList.toggle('hidden', !activeChannel);
  }
  if (!activeChannel) return;

  const slider = document.getElementById('timeshift-slider');
  const offsetText = document.getElementById('timeshift-current-offset');
  const bufStartText = document.getElementById('timeshift-buffer-start');
  const btnLive = document.getElementById('btn-timeshift-live');
  const btnPlayPause = document.getElementById('btn-timeshift-playpause');

  if (btnPlayPause && video) {
    btnPlayPause.textContent = video.paused ? '▶' : '⏸';
  }

  if (isScrubbingTimeshift || !slider || !offsetText || !bufStartText || !btnLive) return;

  const state = getUnifiedTimeshiftState();
  if (!state.hasBuffer) {
    slider.min = 0;
    slider.max = 100;
    slider.value = 100;
    offsetText.textContent = 'LIVE';
    bufStartText.textContent = '-00:00';
    btnLive.classList.add('is-live');
    return;
  }

  slider.min = 0;
  slider.max = Math.round(state.totalDuration);
  slider.value = Math.round(state.totalDuration - state.currentOffset);

  // Auto Live-Chasing when at live edge
  if (!isUserTimeshifted && video && !video.paused && !isScrubbingTimeshift && state.isLive) {
    if (state.currentOffset > 1.8 && state.currentOffset < 5.0) {
      if (video.playbackRate !== 1.05) video.playbackRate = 1.05;
    } else if (state.currentOffset >= 5.0) {
      seekToTimeshiftOffset(0);
      video.playbackRate = 1.0;
    } else if (state.currentOffset <= 0.8 && video.playbackRate !== 1.0) {
      video.playbackRate = 1.0;
    }
  } else if (isUserTimeshifted && video && video.playbackRate !== 1.0) {
    video.playbackRate = 1.0;
  }

  btnLive.classList.toggle('is-live', state.isLive);
  offsetText.textContent = state.isLive ? 'LIVE' : formatTimeshiftOffset(state.currentOffset);
  bufStartText.textContent = formatTimeshiftOffset(state.totalDuration);
}

function toggleTimeshiftPlayPause() {
  if (!video) return;
  if (video.paused) {
    video.play().catch(e => console.log('[Timeshift] Resume error:', e));
  } else {
    isUserTimeshifted = true;
    video.pause();
  }
  updateTimeshiftUI();
  showPlayerUI();
}

function seekTimeshift(deltaSeconds) {
  const state = getUnifiedTimeshiftState();
  if (!state.hasBuffer) return;
  // deltaSeconds > 0 means jump forward (+10s closer to live)
  // deltaSeconds < 0 means jump backward (-10s further from live)
  const targetOffset = Math.max(0, state.currentOffset - deltaSeconds);
  seekToTimeshiftOffset(targetOffset);
}

function jumpToLive() {
  seekToTimeshiftOffset(0);
}

function initTimeshift() {
  const btnPlayPause = document.getElementById('btn-timeshift-playpause');
  const btnRewind = document.getElementById('btn-timeshift-rewind');
  const btnForward = document.getElementById('btn-timeshift-forward');
  const btnLive = document.getElementById('btn-timeshift-live');
  const slider = document.getElementById('timeshift-slider');
  const offsetText = document.getElementById('timeshift-current-offset');

  if (btnPlayPause) btnPlayPause.onclick = toggleTimeshiftPlayPause;
  if (btnRewind) btnRewind.onclick = () => seekTimeshift(-10);
  if (btnForward) btnForward.onclick = () => seekTimeshift(10);
  if (btnLive) btnLive.onclick = jumpToLive;

  if (slider) {
    slider.addEventListener('input', () => {
      isScrubbingTimeshift = true;
      const max = parseFloat(slider.max) || 100;
      const val = parseFloat(slider.value);
      const offset = Math.max(0, max - val);
      if (offsetText) {
        offsetText.textContent = offset < 2.5 ? 'LIVE' : formatTimeshiftOffset(offset);
      }
    });

    slider.addEventListener('change', () => {
      isScrubbingTimeshift = false;
      const max = parseFloat(slider.max) || 100;
      const val = parseFloat(slider.value);
      const targetOffset = Math.max(0, max - val);
      seekToTimeshiftOffset(targetOffset);
    });
  }

  updateTimeshiftUI();
}

// ==========================================================
// REAL-TIME RF SIGNAL ANALYZER HUD
// ==========================================================
function renderRFAnalyzer(sig) {
  const overlay = document.getElementById('rf-analyzer-overlay');
  if (!overlay || overlay.classList.contains('hidden')) return;

  const ch = activeChannel;
  const activeChannelEl = document.getElementById('rf-active-channel');
  const muxFreqEl = document.getElementById('rf-mux-freq');
  const muxDelivEl = document.getElementById('rf-mux-deliv');
  const muxBwEl = document.getElementById('rf-mux-bw');
  const tunerIdEl = document.getElementById('rf-tuner-id');

  if (ch) {
    if (activeChannelEl) activeChannelEl.textContent = `${ch.number}. ${ch.name}`;
    if (muxFreqEl) muxFreqEl.textContent = `${(ch.frequency / 1000000).toFixed(1)} MHz`;
    if (muxDelivEl) muxDelivEl.textContent = ch.delivery_system || 'DVB-T/T2';
    if (muxBwEl) muxBwEl.textContent = ch.bandwidth_hz ? `${ch.bandwidth_hz / 1000000} MHz` : '8 MHz';
  } else {
    if (activeChannelEl) activeChannelEl.textContent = 'No Channel Active';
    if (muxFreqEl) muxFreqEl.textContent = '-- MHz';
  }

  if (tunerIdEl && currentTuners) {
    const act = currentTuners.find(t => t.status === 'active' && (!ch || t.current_frequency === ch.frequency));
    tunerIdEl.textContent = act ? act.name : (currentTuners[0] ? currentTuners[0].name : 'Tuner 0');
  }

  if (!sig) return;

  const str = sig.signal_strength || 0;
  const q = sig.signal_quality || 0;
  const snr = sig.snr_db !== undefined ? sig.snr_db.toFixed(1) : null;

  const barStr = document.getElementById('rf-bar-strength');
  const valStr = document.getElementById('rf-val-strength');
  const barQ = document.getElementById('rf-bar-quality');
  const valQ = document.getElementById('rf-val-quality');
  const valSnr = document.getElementById('rf-val-snr');
  const valBer = document.getElementById('rf-val-ber');
  const valUnc = document.getElementById('rf-val-unc');

  if (barStr) barStr.style.width = `${Math.min(100, Math.max(0, str))}%`;
  if (valStr) valStr.textContent = `${str}%`;
  if (barQ) barQ.style.width = `${Math.min(100, Math.max(0, q))}%`;
  if (valQ) valQ.textContent = `${q}%`;
  if (valSnr) valSnr.textContent = snr ? `(${snr} dB)` : '(-- dB)';
  if (valBer) valBer.textContent = (sig.ber !== undefined) ? sig.ber : '0';
  if (valUnc) valUnc.textContent = (sig.unc !== undefined) ? sig.unc : '0';

  const flagSig = document.getElementById('rf-flag-sig');
  const flagCarrier = document.getElementById('rf-flag-carrier');
  const flagViterbi = document.getElementById('rf-flag-viterbi');
  const flagSync = document.getElementById('rf-flag-sync');
  const flagLock = document.getElementById('rf-flag-lock');

  if (flagSig) {
    flagSig.classList.toggle('active', !!sig.has_signal);
    flagSig.classList.toggle('locked', !!sig.has_signal);
  }
  if (flagCarrier) {
    flagCarrier.classList.toggle('active', !!sig.has_carrier);
    flagCarrier.classList.toggle('locked', !!sig.has_carrier);
  }
  if (flagViterbi) {
    flagViterbi.classList.toggle('active', !!sig.has_viterbi);
    flagViterbi.classList.toggle('locked', !!sig.has_viterbi);
  }
  if (flagSync) {
    flagSync.classList.toggle('active', !!sig.has_sync);
    flagSync.classList.toggle('locked', !!sig.has_sync);
  }
  if (flagLock) {
    flagLock.classList.toggle('active', !!sig.has_lock);
    flagLock.classList.toggle('locked', !!sig.has_lock);
  }
}

function toggleRFAnalyzer(force) {
  const el = document.getElementById('rf-analyzer-overlay');
  if (!el) return;
  const wrapper = document.getElementById('video-wrapper');
  if (wrapper && el.parentElement !== wrapper) {
    wrapper.appendChild(el);
  }
  const show = typeof force === 'boolean' ? force : el.classList.contains('hidden');
  el.classList.toggle('hidden', !show);
  if (show) {
    renderRFAnalyzer(currentSignal);
  }
}
window.toggleRFAnalyzer = toggleRFAnalyzer;

// ==========================================================
// TELETEXT VIEWER & SUBTITLES
// ==========================================================
let currentTeletextPage = 100;
let teletextClockTimer = null;

function updateTeletextClock() {
  const el = document.getElementById('txt-live-time');
  if (el) el.textContent = new Date().toLocaleTimeString();
}

function toggleTeletext(force) {
  const el = document.getElementById('teletext-overlay');
  if (!el) return;
  const hasTxt = !!(availableChannelTracks && availableChannelTracks.has_teletext);
  if (!hasTxt && force !== false && el.classList.contains('hidden')) {
    showToast('ℹ️ Teletext is not broadcast on this channel', 'info');
    return;
  }
  const wrapper = document.getElementById('video-wrapper');
  if (wrapper && el.parentElement !== wrapper) {
    wrapper.appendChild(el);
  }
  const show = typeof force === 'boolean' ? force : el.classList.contains('hidden');
  el.classList.toggle('hidden', !show);
  if (show) {
    renderTeletextScreen(currentTeletextPage);
    if (!teletextClockTimer) {
      teletextClockTimer = setInterval(updateTeletextClock, 1000);
      updateTeletextClock();
    }
  } else {
    if (teletextClockTimer) {
      clearInterval(teletextClockTimer);
      teletextClockTimer = null;
    }
  }
}
window.toggleTeletext = toggleTeletext;

function navTeletextPage(pageNum) {
  currentTeletextPage = Math.min(899, Math.max(100, parseInt(pageNum, 10) || 100));
  const input = document.getElementById('teletext-page-input');
  const pnum = document.getElementById('txt-page-num');
  if (input) input.value = currentTeletextPage;
  if (pnum) pnum.textContent = currentTeletextPage;
  renderTeletextScreen(currentTeletextPage);
}
window.navTeletextPage = navTeletextPage;

function navTeletextInput() {
  const input = document.getElementById('teletext-page-input');
  if (input) navTeletextPage(input.value);
}
window.navTeletextInput = navTeletextInput;

function renderTeletextScreen(page) {
  const screen = document.getElementById('teletext-screen');
  const chName = document.getElementById('txt-channel-name');
  if (!screen) return;

  const ch = activeChannel ? activeChannel.name.toUpperCase() : 'DVB BROADCAST';
  if (chName) chName.textContent = ch;

  let body = '';
  if (page === 100) {
    body = `
<div class="txt-row txt-cyan">========================================</div>
<div class="txt-row txt-yellow">         DVB-WEB TELETEXT SERVICE       </div>
<div class="txt-row txt-cyan">========================================</div>
<div class="txt-row">Channel: <span class="txt-white">${ch}</span></div>
<div class="txt-row">System:  <span class="txt-green">ETSI EN 300 706 / DVB-TXT</span></div>
<div class="txt-row">Status:  <span class="txt-green">ACTIVE CARRIER</span></div>
<br>
<div class="txt-row txt-yellow">--- MAIN DIRECTORY ---</div>
<div class="txt-row"><span class="txt-red">100</span> Main Index & Welcome</div>
<div class="txt-row"><span class="txt-green">200</span> Program Guide / EPG Highlights</div>
<div class="txt-row"><span class="txt-yellow">300</span> Multiplex & Technical Info</div>
<div class="txt-row"><span class="txt-blue">888</span> Subtitles / Closed Captions</div>
<br>
<div class="txt-row txt-cyan">Navigation: Press color buttons below</div>
<div class="txt-row txt-cyan">or enter 3-digit page number (100-899).</div>
`;
  } else if (page === 200) {
    const nowEv = currentEPGNowNext ? currentEPGNowNext.now : null;
    const nextEv = currentEPGNowNext ? currentEPGNowNext.next : null;
    const nowTitle = nowEv ? (getLocalized(nowEv.titles, currentLanguage) || 'Live Broadcast') : 'Regular Programming';
    const nextTitle = nextEv ? (getLocalized(nextEv.titles, currentLanguage) || 'Upcoming Broadcast') : 'Follows Later';
    body = `
<div class="txt-row txt-green">========================================</div>
<div class="txt-row txt-yellow">         PROGRAM GUIDE / EPG            </div>
<div class="txt-row txt-green">========================================</div>
<br>
<div class="txt-row txt-yellow">NOW ON AIR:</div>
<div class="txt-row txt-white">> ${nowTitle}</div>
${nowEv ? `<div class="txt-row txt-cyan">  Time: ${new Date(nowEv.start_time).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'})} - ${new Date(nowEv.end_time).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'})}</div>` : ''}
<br>
<div class="txt-row txt-yellow">NEXT UP:</div>
<div class="txt-row txt-white">> ${nextTitle}</div>
${nextEv ? `<div class="txt-row txt-cyan">  Time: ${new Date(nextEv.start_time).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'})} - ${new Date(nextEv.end_time).toLocaleTimeString([], {hour:'2-digit', minute:'2-digit'})}</div>` : ''}
<br>
<div class="txt-row txt-white">Press <span class="txt-red">100</span> to return to Index.</div>
`;
  } else if (page === 300) {
    const freq = activeChannel ? (activeChannel.frequency / 1000000).toFixed(1) : '--';
    const deliv = activeChannel ? (activeChannel.delivery_system || 'DVB-T/T2') : 'DVB';
    const sig = currentSignal ? `${currentSignal.signal_quality || 0}% (Str: ${currentSignal.signal_strength || 0}%)` : 'Locked';
    body = `
<div class="txt-row txt-yellow">========================================</div>
<div class="txt-row txt-cyan">         TECHNICAL MULTIPLEX SPECS      </div>
<div class="txt-row txt-yellow">========================================</div>
<br>
<div class="txt-row">Multiplex Frequency : <span class="txt-white">${freq} MHz</span></div>
<div class="txt-row">Standard Delivery   : <span class="txt-white">${deliv}</span></div>
<div class="txt-row">RF Signal Quality   : <span class="txt-green">${sig}</span></div>
<div class="txt-row">Audio Tracks        : <span class="txt-white">${(availableChannelTracks && availableChannelTracks.audio_tracks) ? availableChannelTracks.audio_tracks.length : 1} available</span></div>
<div class="txt-row">Subtitle Streams    : <span class="txt-white">${(availableChannelTracks && availableChannelTracks.subtitle_tracks) ? availableChannelTracks.subtitle_tracks.length : 0} available</span></div>
<br>
<div class="txt-row txt-white">Teletext Page Buffer: Synchronized</div>
<div class="txt-row txt-white">Press <span class="txt-red">100</span> to return to Index.</div>
`;
  } else if (page === 888) {
    const hasSub = availableChannelTracks && availableChannelTracks.subtitle_tracks && availableChannelTracks.subtitle_tracks.length > 0;
    body = `
<div class="txt-row txt-blue">========================================</div>
<div class="txt-row txt-yellow">   TELETEXT SUBTITLES / CLOSED CAPTIONS </div>
<div class="txt-row txt-blue">========================================</div>
<br>
<div class="txt-row txt-white">PAGE 888 SUBTITLE DECODER:</div>
<div class="txt-row txt-green">STATUS: READY</div>
<br>
<div class="txt-row">DVB Subtitle Streams: <span class="txt-white">${hasSub ? availableChannelTracks.subtitle_tracks.length + ' detected' : 'Standard'}</span></div>
<div class="txt-row">Active Track: <span class="txt-cyan">${currentSubTrack === -1 ? 'Off' : 'Track #' + currentSubTrack}</span></div>
<br>
<div class="txt-row txt-yellow">Action:</div>
<div class="txt-row"><button class="btn btn-secondary btn-sm" onclick="toggleTeletextSubtitles()">Toggle Burned-in Subtitles</button></div>
<br>
<div class="txt-row txt-cyan">Teletext subtitles are rendered in sync</div>
<div class="txt-row txt-cyan">with the video stream.</div>
<br>
<div class="txt-row txt-white">Press <span class="txt-red">100</span> to return to Index.</div>
`;
  } else {
    body = `
<div class="txt-row txt-cyan">========================================</div>
<div class="txt-row txt-yellow">         TELETEXT PAGE ${page}          </div>
<div class="txt-row txt-cyan">========================================</div>
<br>
<div class="txt-row txt-white">Page ${page} transmission broadcasted.</div>
<div class="txt-row">Channel: <span class="txt-green">${ch}</span></div>
<br>
<div class="txt-row txt-yellow">No further sub-pages on this PID.</div>
<br>
<div class="txt-row txt-white">Press <span class="txt-red">100</span> for Main Index.</div>
`;
  }

  screen.innerHTML = body;
}

function toggleTeletextSubtitles() {
  if (availableChannelTracks && availableChannelTracks.subtitle_tracks && availableChannelTracks.subtitle_tracks.length > 0) {
    if (currentSubTrack === -1) {
      currentSubTrack = 0;
    } else {
      currentSubTrack = -1;
    }
    if (activeChannel) {
      startStream(activeChannel);
      showToast(currentSubTrack === -1 ? 'Subtitles turned off' : 'Subtitles turned on (Track 0)', 'info');
    }
  } else {
    showToast('No separate DVB subtitle PID detected for this channel', 'info');
  }
}
window.toggleTeletextSubtitles = toggleTeletextSubtitles;

// ==========================================================
// HBBTV INTERACTIVE PORTAL
// ==========================================================
let hbbtvMutedVideo = false;

function syncPlayerMuteUI() {
  const vid = document.getElementById('tv-video');
  const btnMute = document.getElementById('btn-mute');
  const volGroup = document.querySelector('.vol-group');
  if (!vid) return;
  const isMuted = vid.muted || vid.volume === 0;
  if (btnMute) btnMute.textContent = isMuted ? '🔇' : (vid.volume < 0.5 ? '🔉' : '🔊');
  if (volGroup) volGroup.classList.toggle('is-muted', isMuted);
}

function toggleHbbTV(force) {
  const modal = document.getElementById('hbbtv-modal');
  if (!modal) return;
  const show = typeof force === 'boolean' ? force : modal.classList.contains('hidden');
  if (!show) {
    modal.classList.add('hidden');
    const iframe = document.getElementById('hbbtv-iframe');
    if (iframe) iframe.src = 'about:blank';
    if (hbbtvMutedVideo) {
      const v1 = document.getElementById('tv-video');
      const v2 = document.getElementById('tv-video-next');
      if (v1) v1.muted = false;
      if (v2) v2.muted = false;
      hbbtvMutedVideo = false;
      syncPlayerMuteUI();
    }
    return;
  }
  const url = (availableChannelTracks && availableChannelTracks.hbbtv_url) || (activeChannel && activeChannel.hbbtv_url) || '';
  if (!url) {
    showToast('ℹ️ HbbTV is not supported or signaled by this channel', 'info');
    return;
  }
  const targetParent = document.fullscreenElement || document.body;
  if (modal.parentElement !== targetParent) {
    targetParent.appendChild(modal);
  }
  const iframe = document.getElementById('hbbtv-iframe');
  const title = document.getElementById('hbbtv-modal-title');
  if (iframe) iframe.src = `/api/hbbtv/proxy?url=${encodeURIComponent(url)}`;
  if (title && activeChannel) title.textContent = `🔴 HbbTV: ${activeChannel.name}`;
  modal.classList.remove('hidden');

  // Mute background TV playback while HbbTV interactive portal is open
  const v1 = document.getElementById('tv-video');
  const v2 = document.getElementById('tv-video-next');
  if (v1 && !v1.muted) {
    v1.muted = true;
    hbbtvMutedVideo = true;
  }
  if (v2) v2.muted = true;
  syncPlayerMuteUI();

  const wrapper = document.getElementById('video-wrapper');
  if (wrapper) wrapper.classList.remove('cursor-hidden');
  showPlayerUI();
}
window.toggleHbbTV = toggleHbbTV;

function sendHbbtvKeyToIframe(keyCode) {
  const iframe = document.getElementById('hbbtv-iframe');
  if (!iframe) return;
  try {
    const targetDoc = iframe.contentDocument || (iframe.contentWindow && iframe.contentWindow.document);
    const targetWin = iframe.contentWindow;
    if (!targetWin) return;

    if (typeof targetWin.sendHbbtvKey === 'function') {
      targetWin.sendHbbtvKey(keyCode);
    } else {
      const evt = new (targetWin.KeyboardEvent || KeyboardEvent)('keydown', {
        keyCode: keyCode,
        which: keyCode,
        bubbles: true,
        cancelable: true
      });
      evt._isSyntheticHbbtvKey = true;
      Object.defineProperty(evt, 'keyCode', { value: keyCode });
      Object.defineProperty(evt, 'which', { value: keyCode });
      if (targetDoc) targetDoc.dispatchEvent(evt);
      targetWin.dispatchEvent(evt);
    }
  } catch (e) {
    console.warn('Could not dispatch key into HbbTV iframe:', e);
  }

  // Remove focus from button and return focus to main page so keyboard shortcuts continue seamlessly
  if (document.activeElement && typeof document.activeElement.blur === 'function') {
    document.activeElement.blur();
  }
  window.focus();
}
window.sendHbbtvKeyToIframe = sendHbbtvKeyToIframe;

// ==========================================================
// KEYBOARD SHORTCUTS HELP MODAL
// ==========================================================
function toggleShortcutsModal(force) {
  const modal = document.getElementById('shortcuts-modal');
  if (!modal) return;
  const show = typeof force === 'boolean' ? force : modal.classList.contains('hidden');
  if (show) {
    const targetParent = document.fullscreenElement || document.body;
    if (modal.parentElement !== targetParent) {
      targetParent.appendChild(modal);
    }
    const wrapper = document.getElementById('video-wrapper');
    if (wrapper) wrapper.classList.remove('cursor-hidden');
    showPlayerUI();
  }
  modal.classList.toggle('hidden', !show);
}
window.toggleShortcutsModal = toggleShortcutsModal;

function setupInteractiveFeatures() {
  const btnCloseShortcuts = document.getElementById('btn-close-shortcuts');
  if (btnCloseShortcuts) btnCloseShortcuts.onclick = () => toggleShortcutsModal(false);
  const shortcutsModal = document.getElementById('shortcuts-modal');
  if (shortcutsModal) {
    shortcutsModal.onclick = (e) => {
      if (e.target.id === 'shortcuts-modal') toggleShortcutsModal(false);
    };
  }

  const btnCloseHbbtv = document.getElementById('btn-close-hbbtv');
  if (btnCloseHbbtv) btnCloseHbbtv.onclick = () => toggleHbbTV(false);
  const hbbtvModal = document.getElementById('hbbtv-modal');
  if (hbbtvModal) {
    hbbtvModal.onclick = (e) => {
      if (e.target.id === 'hbbtv-modal') toggleHbbTV(false);
    };
  }

  // Player Control Bar Buttons
  const btnTeletextCtrl = document.getElementById('btn-teletext-ctrl');
  if (btnTeletextCtrl) {
    btnTeletextCtrl.onclick = (e) => {
      e.stopPropagation();
      toggleTeletext();
    };
  }

  const btnHbbtvCtrl = document.getElementById('btn-hbbtv-ctrl');
  if (btnHbbtvCtrl) {
    btnHbbtvCtrl.onclick = (e) => {
      e.stopPropagation();
      toggleHbbTV();
    };
  }

  // Keep player overlays inside #video-wrapper, and keep global modals/toasts inside fullscreen element when active
  document.addEventListener('fullscreenchange', () => {
    const videoWrapper = document.getElementById('video-wrapper');
    const isFullscreen = !!document.fullscreenElement;

    // Player HUD overlays MUST always reside inside the video surface container
    const videoTarget = document.getElementById('video-screen') || videoWrapper;
    ['teletext-overlay', 'rf-analyzer-overlay'].forEach(id => {
      const el = document.getElementById(id);
      if (el && videoTarget && el.parentElement !== videoTarget) {
        videoTarget.appendChild(el);
      }
    });

    // Modals and toasts move into videoWrapper during fullscreen so they are visible, and return to document.body outside fullscreen
    const modalTarget = isFullscreen ? videoWrapper : document.body;
    ['shortcuts-modal', 'hbbtv-modal', 'epg-modal', 'conflict-modal', 'app-toast'].forEach(id => {
      const el = document.getElementById(id);
      if (el && el.parentElement !== modalTarget) {
        modalTarget.appendChild(el);
      }
    });
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
      const shortcutsModal = document.getElementById('shortcuts-modal');
      if (shortcutsModal && !shortcutsModal.classList.contains('hidden')) {
        toggleShortcutsModal(false);
        return;
      }
      const hbbtvModal = document.getElementById('hbbtv-modal');
      if (hbbtvModal && !hbbtvModal.classList.contains('hidden')) {
        toggleHbbTV(false);
        return;
      }
      const teletextOverlay = document.getElementById('teletext-overlay');
      if (teletextOverlay && !teletextOverlay.classList.contains('hidden')) {
        toggleTeletext(false);
        return;
      }
      const rfOverlay = document.getElementById('rf-analyzer-overlay');
      if (rfOverlay && !rfOverlay.classList.contains('hidden')) {
        toggleRFAnalyzer(false);
        return;
      }
      const manualSched = document.getElementById('manual-schedule-modal');
      if (manualSched && !manualSched.classList.contains('hidden')) {
        closeManualScheduleModal();
        return;
      }
      const recPlayer = document.getElementById('recording-player-modal');
      if (recPlayer && !recPlayer.classList.contains('hidden')) {
        closeRecordingPlayer();
        return;
      }
      closeEPGModal();
      closeConflictModal();
      return;
    }

    const hbbtvModal = document.getElementById('hbbtv-modal');
    if (hbbtvModal && !hbbtvModal.classList.contains('hidden')) {
      if (e.key === 'h' || e.key === 'H') {
        e.preventDefault();
        toggleHbbTV(false);
        return;
      }
      if (e.key === 'r' || e.key === 'R' || e.key === '1') {
        e.preventDefault();
        sendHbbtvKeyToIframe(403);
        return;
      }
      if (e.key === 'g' || e.key === 'G' || e.key === '2') {
        e.preventDefault();
        sendHbbtvKeyToIframe(404);
        return;
      }
      if (e.key === 'y' || e.key === 'Y' || e.key === '3') {
        e.preventDefault();
        sendHbbtvKeyToIframe(405);
        return;
      }
      if (e.key === 'b' || e.key === 'B' || e.key === '4') {
        e.preventDefault();
        sendHbbtvKeyToIframe(406);
        return;
      }
      if (['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Enter', 'Backspace'].includes(e.key)) {
        e.preventDefault();
        const codeMap = {
          ArrowUp: 38,
          ArrowDown: 40,
          ArrowLeft: 37,
          ArrowRight: 39,
          Enter: 13,
          Backspace: 461
        };
        sendHbbtvKeyToIframe(codeMap[e.key] || e.keyCode);
        return;
      }
    }

    if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT' || e.target.tagName === 'TEXTAREA') return;

    if (e.key === '?' || (e.key === '/' && e.shiftKey)) {
      e.preventDefault();
      toggleShortcutsModal();
    } else if ((e.key === 's' || e.key === 'S') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      toggleRFAnalyzer();
    } else if ((e.key === 't' || e.key === 'T') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      toggleTeletext();
    } else if ((e.key === 'h' || e.key === 'H') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      toggleHbbTV();
    } else if (e.code === 'Space' || ((e.key === 'k' || e.key === 'K') && !e.ctrlKey && !e.metaKey && !e.altKey)) {
      e.preventDefault();
      toggleTimeshiftPlayPause();
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault();
      seekTimeshift(-10);
    } else if (e.key === 'ArrowRight') {
      e.preventDefault();
      seekTimeshift(10);
    } else if ((e.key === 'l' || e.key === 'L') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      jumpToLive();
    } else if ((e.key === 'm' || e.key === 'M') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      document.getElementById('btn-mute').click();
    } else if (e.key === '+' || e.key === '=') {
      e.preventDefault();
      adjustVolume(0.05);
    } else if (e.key === '-' || e.key === '_') {
      e.preventDefault();
      adjustVolume(-0.05);
    } else if (e.key === '[' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      adjustSubtitleOffset(-500);
    } else if (e.key === ']' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      adjustSubtitleOffset(500);
    } else if ((e.key === 'a' || e.key === 'A') && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault();
      cycleAspectRatio();
    } else if (e.key === 'ArrowUp' || e.key === 'PageUp') {
      zapDelta(-1);
    } else if (e.key === 'ArrowDown' || e.key === 'PageDown') {
      zapDelta(1);
    } else if ((e.key === 'f' || e.key === 'F') && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      document.getElementById('btn-fullscreen').click();
    } else if ((e.key === 'i' || e.key === 'I') && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      const modal = document.getElementById('epg-modal');
      if (modal && !modal.classList.contains('hidden')) {
        closeEPGModal();
      } else {
        openLiveProgramDetails();
      }
    } else if ((e.key === 'n' || e.key === 'N') && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      const modal = document.getElementById('epg-modal');
      if (modal && !modal.classList.contains('hidden')) {
        closeEPGModal();
      } else {
        openNextProgramDetails();
      }
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

let epgTimeFormatter = null;
function formatEPGTime(date) {
  if (!epgTimeFormatter) {
    epgTimeFormatter = new Intl.DateTimeFormat([], { hour: '2-digit', minute: '2-digit' });
  }
  return epgTimeFormatter.format(date);
}

async function loadEPGTimeline(isInitial = false) {
  // Query up to 14 days of EPG (supports standard 7-day, 10-day, and 14-day extended guides)
  const maxQueryDays = 14;
  if (!timelineStartTime || isInitial) {
    const now = new Date();
    // Timeline starts 2 hours ago
    timelineStartTime = new Date(now.getTime() - 2 * 60 * 60 * 1000);
    timelineStartTime.setMinutes(0, 0, 0); // align to hour
  }
  const maxQueryEndTime = new Date(timelineStartTime.getTime() + maxQueryDays * 24 * 60 * 60 * 1000);

  const gridScroll = document.getElementById('epg-grid-scroll');
  const prevScrollLeft = gridScroll ? gridScroll.scrollLeft : 0;
  const prevScrollTop = gridScroll ? gridScroll.scrollTop : 0;

  // Fetch ALL channels' EPG in a single batch HTTP request
  let allEvents = [];
  try {
    const res = await fetch(`/api/epg?from=${timelineStartTime.toISOString()}&to=${maxQueryEndTime.toISOString()}`);
    allEvents = await res.json() || [];
  } catch (err) {
    console.error('Failed to load EPG events:', err);
  }

  // Determine the actual end of EPG data dynamically based on the events returned
  let maxEventTime = timelineStartTime.getTime();
  if (allEvents && allEvents.length > 0) {
    for (let i = 0; i < allEvents.length; i++) {
      const eEnd = new Date(allEvents[i].end_time).getTime();
      if (!isNaN(eEnd) && eEnd > maxEventTime) {
        maxEventTime = eEnd;
      }
    }
  }

  // Calculate timeline end time aligned to the end of the last day with programs (or at least 24h)
  const minEndTime = new Date(timelineStartTime.getTime() + 24 * 60 * 60 * 1000);
  const endOfLastDay = new Date(maxEventTime);
  endOfLastDay.setHours(24, 0, 0, 0);

  timelineEndTime = endOfLastDay > minEndTime ? endOfLastDay : minEndTime;
  const totalMinutes = (timelineEndTime - timelineStartTime) / (60 * 1000);
  const hoursCount = Math.ceil(totalMinutes / 60);

  updateCachedLayoutMetrics();
  renderDaySelector(timelineStartTime, hoursCount, allEvents);
  renderTimeRuler(timelineStartTime, hoursCount);
  renderChannelsColumn();
  await renderProgramRows(timelineStartTime, timelineEndTime, allEvents);
  updateNowMarkerPosition();
  setupEPGScrolling();
  lastEPGLoadTime = Date.now();

  if (isInitial) {
    setTimeout(jumpToNow, 100);
  } else if (gridScroll) {
    gridScroll.scrollLeft = prevScrollLeft;
    gridScroll.scrollTop = prevScrollTop;
    updateEPGActiveDay();
  }
}

let isProgrammaticDayScroll = false;
let dayScrollTimeout = null;
let timelineTotalWidthPx = 0;
let cachedChColWidth = 130;
let lastEPGScrollLeft = -1;

function updateCachedLayoutMetrics() {
  cachedChColWidth = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--ch-col-width')) || 130;
}

function isSameDay(d1, d2) {
  if (!d1 || !d2) return false;
  return d1.getFullYear() === d2.getFullYear() &&
         d1.getMonth() === d2.getMonth() &&
         d1.getDate() === d2.getDate();
}

function scrollDayButtonIntoView(btn, center = false) {
  const container = document.getElementById('epg-day-selector');
  if (!container || !btn) return;
  const btnLeft = btn.offsetLeft;
  const btnWidth = btn.offsetWidth;
  const btnRight = btnLeft + btnWidth;
  const scrollLeft = container.scrollLeft;
  const visibleWidth = container.clientWidth;

  if (center) {
    const targetScroll = btnLeft - (visibleWidth / 2) + (btnWidth / 2);
    container.scrollTo({ left: Math.max(0, targetScroll), behavior: 'smooth' });
  } else {
    if (btnLeft < scrollLeft) {
      container.scrollTo({ left: btnLeft, behavior: 'smooth' });
    } else if (btnRight > scrollLeft + visibleWidth) {
      container.scrollTo({ left: btnRight - visibleWidth, behavior: 'smooth' });
    }
  }
}

function updateEPGActiveDay() {
  if (isProgrammaticDayScroll) return;

  const gridScroll = document.getElementById('epg-grid-scroll');
  const dayButtons = document.querySelectorAll('.day-btn');
  if (!gridScroll || !timelineStartTime || dayButtons.length === 0) return;

  const scrollLeft = gridScroll.scrollLeft;
  if (scrollLeft === lastEPGScrollLeft) return;
  lastEPGScrollLeft = scrollLeft;

  const maxScrollLeft = Math.max(0, (timelineTotalWidthPx || 40320) - gridScroll.clientWidth);

  let targetBtn = null;

  if (maxScrollLeft > 0 && scrollLeft >= maxScrollLeft - 20) {
    targetBtn = dayButtons[dayButtons.length - 1];
  } else if (scrollLeft <= 5) {
    targetBtn = dayButtons[0];
  } else {
    const eventsVisibleWidth = Math.max(0, gridScroll.clientWidth - cachedChColWidth);
    const centerPx = scrollLeft + (eventsVisibleWidth / 2);
    const centerMinutes = centerPx / PIXELS_PER_MINUTE;
    const targetDate = new Date(timelineStartTime.getTime() + centerMinutes * 60 * 1000);

    targetBtn = Array.from(dayButtons).find(btn => btn._dayDate && isSameDay(btn._dayDate, targetDate));

    if (!targetBtn && dayButtons.length > 0) {
      if (targetDate < dayButtons[0]._dayDate) {
        targetBtn = dayButtons[0];
      } else if (targetDate > dayButtons[dayButtons.length - 1]._dayDate) {
        targetBtn = dayButtons[dayButtons.length - 1];
      }
    }
  }

  if (!targetBtn || targetBtn.classList.contains('active')) return;

  dayButtons.forEach(b => b.classList.remove('active'));
  targetBtn.classList.add('active');
  scrollDayButtonIntoView(targetBtn, false);
}

function renderDaySelector(startTime, hoursCount, allEvents = []) {
  const container = document.getElementById('epg-day-selector');
  if (!container) return;
  container.innerHTML = '';

  const nowZero = new Date();
  nowZero.setHours(0, 0, 0, 0);

  const candidateDays = [];
  const cur = new Date(nowZero); // Days start from Today

  const endDay = new Date(startTime.getTime() + hoursCount * 60 * 60 * 1000);
  while (cur < endDay) {
    candidateDays.push(new Date(cur));
    cur.setDate(cur.getDate() + 1);
  }

  // Pre-calculate which days have at least one EPG event
  const daysWithEPG = candidateDays.filter(day => {
    const dayStart = day.getTime();
    const dayEnd = dayStart + 24 * 60 * 60 * 1000;

    return allEvents && allEvents.some(e => {
      const s = new Date(e.start_time).getTime();
      const end = new Date(e.end_time).getTime();
      return !isNaN(s) && !isNaN(end) && s < dayEnd && end > dayStart;
    });
  });

  // Only contain the days that there is an EPG program.
  // Ensure Today is always included as the anchor day.
  let days = daysWithEPG;
  if (!days.some(d => isSameDay(d, nowZero))) {
    days.unshift(new Date(nowZero));
  }

  days.forEach((day, idx) => {
    const btn = document.createElement('button');
    btn._dayDate = day;
    const diffDays = Math.round((day.getTime() - nowZero.getTime()) / 86400000);
    const isToday = diffDays === 0;
    const isActive = isToday || (idx === 0);
    btn.className = `day-btn ${isActive ? 'active' : ''}`;

    let label = '';
    if (diffDays === 0) {
      label = 'Today';
    } else if (diffDays === 1) {
      label = 'Tomorrow';
    } else if (diffDays === -1) {
      label = 'Yesterday';
    } else {
      label = day.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
    }
    btn.textContent = label;
    btn.onclick = () => {
      isProgrammaticDayScroll = true;
      document.querySelectorAll('.day-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      scrollDayButtonIntoView(btn, true);

      let targetTime = new Date(day);
      targetTime.setHours(8, 0, 0, 0);
      if (targetTime < startTime) targetTime = startTime;
      const minutesSinceStart = (targetTime.getTime() - startTime.getTime()) / (60 * 1000);
      const gridScroll = document.getElementById('epg-grid-scroll');
      gridScroll.scrollTo({ left: Math.max(0, minutesSinceStart * PIXELS_PER_MINUTE), behavior: 'smooth' });

      clearTimeout(dayScrollTimeout);
      dayScrollTimeout = setTimeout(() => {
        isProgrammaticDayScroll = false;
      }, 600);
    };
    container.appendChild(btn);
  });

  updateEPGActiveDay();
}

function setupEPGScrolling() {
  const gridScroll = document.getElementById('epg-grid-scroll');
  const rulerScroll = document.getElementById('epg-ruler-scroll');
  if (!gridScroll || !rulerScroll) return;

  // 1. Sync horizontal scrolling between timeline ruler and grid body via requestAnimationFrame
  let scrollRaf = null;
  gridScroll.onscroll = () => {
    if (!scrollRaf) {
      scrollRaf = requestAnimationFrame(() => {
        if (!window._epgIsDragging) {
          rulerScroll.scrollLeft = gridScroll.scrollLeft;
        }
        updateEPGActiveDay();
        scrollRaf = null;
      });
    }
  };

  // 2. Mouse wheel scrolling
  // Normal vertical and horizontal scrolling are handled natively by the browser compositor.
  // Shift + Wheel scrolls horizontally across time.
  if (!gridScroll._hasWheelListener) {
    gridScroll._hasWheelListener = true;
    gridScroll.addEventListener('wheel', (e) => {
      isProgrammaticDayScroll = false;
      if (e.shiftKey && e.deltaY !== 0) {
        e.preventDefault();
        gridScroll.scrollLeft += e.deltaY;
      }
    }, { passive: false });
  }

  // 3. Touch scroll listener
  if (!gridScroll._hasTouchListener) {
    gridScroll._hasTouchListener = true;
    gridScroll.addEventListener('touchstart', () => {
      isProgrammaticDayScroll = false;
    }, { passive: true });
  }

  // 4. Scrollend listener
  if (!gridScroll._hasScrollEndListener) {
    gridScroll._hasScrollEndListener = true;
    gridScroll.addEventListener('scrollend', () => {
      if (isProgrammaticDayScroll) {
        isProgrammaticDayScroll = false;
        updateEPGActiveDay();
      }
    });
  }

  // 5. Click and Drag to pan freely in ALL directions
  if (!gridScroll._hasDragListener) {
    gridScroll._hasDragListener = true;
    let isDown = false;
    let startX = 0;
    let startY = 0;
    let scrollLeft = 0;
    let scrollTop = 0;
    let dragRaf = null;
    let targetLeft = 0;
    let targetTop = 0;
    const DRAG_THRESHOLD = 5;

    gridScroll.addEventListener('mousedown', (e) => {
      if (e.button !== 0) return;
      isProgrammaticDayScroll = false;
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

    window.addEventListener('blur', () => {
      if (isDown) {
        isDown = false;
        gridScroll.classList.remove('is-dragging');
        window._epgIsDragging = false;
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
        targetLeft = scrollLeft - dx;
        targetTop = scrollTop - dy;
        if (!dragRaf) {
          dragRaf = requestAnimationFrame(() => {
            gridScroll.scrollLeft = targetLeft;
            gridScroll.scrollTop = targetTop;
            rulerScroll.scrollLeft = targetLeft;
            dragRaf = null;
          });
        }
      }
    });
  }

  // 6. Navigation Buttons
  const btnPrev = document.getElementById('btn-epg-prev');
  if (btnPrev) {
    btnPrev.onclick = () => {
      isProgrammaticDayScroll = false;
      gridScroll.scrollBy({ left: -2 * 60 * PIXELS_PER_MINUTE, behavior: 'smooth' });
    };
  }

  const btnNext = document.getElementById('btn-epg-next');
  if (btnNext) {
    btnNext.onclick = () => {
      isProgrammaticDayScroll = false;
      gridScroll.scrollBy({ left: 2 * 60 * PIXELS_PER_MINUTE, behavior: 'smooth' });
    };
  }

  const btnNow = document.getElementById('btn-epg-now');
  if (btnNow) {
    btnNow.onclick = () => {
      isProgrammaticDayScroll = false;
      jumpToNow();
    };
  }
}

function renderTimeRuler(startTime, hoursCount) {
  const ruler = document.getElementById('epg-time-ruler');
  if (!ruler) return;
  ruler.innerHTML = '';
  const totalMinutes = hoursCount * 60;
  ruler.style.width = `${totalMinutes * PIXELS_PER_MINUTE}px`;

  const fragment = document.createDocumentFragment();

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

    const timeText = document.createTextNode(formatEPGTime(slotTime));
    slot.appendChild(timeText);
    fragment.appendChild(slot);
  }
  ruler.appendChild(fragment);
}

function renderChannelsColumn() {
  const col = document.getElementById('epg-channels-column');
  if (!col) return;
  col.innerHTML = '';
  const fragment = document.createDocumentFragment();

  channels.forEach(c => {
    const cell = document.createElement('div');
    cell.dataset.channelId = c.id;
    const conflict = getChannelConflict(c);
    const isPlaying = activeChannel && activeChannel.id === c.id;
    cell.className = `epg-channel-cell ${conflict ? 'has-conflict' : ''} ${isPlaying ? 'is-playing' : ''}`;
    cell.title = conflict ? `${c.name} — ${conflict.reason}` : (isPlaying ? `${c.name} (Currently Watching)` : c.name);
    cell.onclick = () => {
      zapToChannel(c);
      if (!conflict) {
        window.location.hash = '#/tv';
      }
    };

    const conflictIndicator = conflict ? 
      `<span class="epg-conflict-icon" title="${conflict.reason}">⚠️</span>` : '';
    const radioTag = c.is_radio ? ' <span title="Radio Station" style="font-size:0.75rem;">📻</span>' : '';
    const playingBadge = isPlaying ? ' <span class="epg-playing-badge" title="Currently Playing">▶ Live</span>' : '';

    cell.innerHTML = `
      <div class="epg-ch-cell-info">
        <div class="epg-ch-cell-name" title="${c.number}. ${c.name}"><span class="badge" style="font-size:0.7rem; padding:0.12rem 0.35rem; margin-right:0.25rem;">${c.number}</span><span class="epg-ch-name-text">${escapeHtml(c.name)}</span>${radioTag} ${conflictIndicator}${playingBadge}</div>
      </div>
    `;
    fragment.appendChild(cell);
  });
  col.appendChild(fragment);
}

function updateEPGPlayingHighlight() {
  const activeId = activeChannel ? activeChannel.id : null;
  document.querySelectorAll('.epg-channel-cell').forEach(cell => {
    const chId = Number(cell.dataset.channelId);
    const isPlaying = activeId !== null && chId === activeId;
    cell.classList.toggle('is-playing', isPlaying);

    const nameEl = cell.querySelector('.epg-ch-cell-name');
    let badge = cell.querySelector('.epg-playing-badge');
    if (isPlaying) {
      if (!badge && nameEl) {
        badge = document.createElement('span');
        badge.className = 'epg-playing-badge';
        badge.title = 'Currently Playing';
        badge.innerHTML = '▶ Live';
        nameEl.appendChild(badge);
      }
    } else if (badge) {
      badge.remove();
    }
  });

  document.querySelectorAll('.epg-channel-row-track').forEach(row => {
    const chId = Number(row.dataset.channelId);
    row.classList.toggle('is-playing', activeId !== null && chId === activeId);
  });
}

async function renderProgramRows(startTime, endTime, prefetchedEvents = null) {
  const container = document.getElementById('epg-rows-container');
  if (!container) return;

  const totalMinutes = (endTime - startTime) / (60 * 1000);
  const totalWidthPx = totalMinutes * PIXELS_PER_MINUTE;
  timelineTotalWidthPx = totalWidthPx;
  const canvas = document.getElementById('epg-events-canvas');
  if (canvas) canvas.style.width = `${totalWidthPx}px`;

  // Fetch ALL channels' EPG in a single batch HTTP request instead of N sequential fetches (use prefetched if provided)
  let allEvents = prefetchedEvents;
  if (!allEvents) {
    try {
      const res = await fetch(`/api/epg?from=${startTime.toISOString()}&to=${endTime.toISOString()}`);
      allEvents = await res.json() || [];
    } catch (err) {
      console.error('Failed to load EPG events:', err);
      allEvents = [];
    }
  }

  // Pre-index events by channel_id
  const eventsByChannel = new Map();
  for (let i = 0; i < allEvents.length; i++) {
    const e = allEvents[i];
    let list = eventsByChannel.get(e.channel_id);
    if (!list) {
      list = [];
      eventsByChannel.set(e.channel_id, list);
    }
    list.push(e);
  }

  // Pre-index recordings by event_id and channel+time for O(1) matching
  const recMap = new Map();
  if (Array.isArray(currentRecordings)) {
    for (const r of currentRecordings) {
      if (r.event_id) recMap.set(`ev_${r.channel_id}_${r.event_id}`, r);
      if (r.start_time) recMap.set(`time_${r.channel_id}_${new Date(r.start_time).getTime()}`, r);
    }
  }

  const nowMs = Date.now();
  const fragment = document.createDocumentFragment();

  for (const c of channels) {
    const row = document.createElement('div');
    row.dataset.channelId = c.id;
    const isPlaying = activeChannel && activeChannel.id === c.id;
    row.className = `epg-channel-row-track ${isPlaying ? 'is-playing' : ''}`;
    row.style.width = `${totalWidthPx}px`;

    const rawEvents = eventsByChannel.get(c.id) || [];

    // Parse, filter valid ranges, and sort strictly chronologically by start_time
    const sortedEvents = [];
    for (let i = 0; i < rawEvents.length; i++) {
      const e = rawEvents[i];
      const s = new Date(e.start_time);
      const end = new Date(e.end_time);
      if (!isNaN(s) && !isNaN(end) && end > startTime && s < endTime && end > s) {
        e._start = s;
        e._end = end;
        sortedEvents.push(e);
      }
    }
    // Sort primarily by start time, and secondarily shorter duration first (real shows before broad placeholders)
    sortedEvents.sort((a, b) => a._start - b._start || (a._end - a._start) - (b._end - b._start));

    // Deduplicate and resolve overlapping programs on the same channel
    const validEvents = [];
    let prevEv = null;
    for (let i = 0; i < sortedEvents.length; i++) {
      const e = sortedEvents[i];
      const durMinutes = (e._end - e._start) / 60000;
      if (durMinutes <= 1 || durMinutes > 24 * 60) continue; // Skip corrupt or absurd >24h spans

      if (prevEv) {
        const diffStartMs = Math.abs(e._start.getTime() - prevEv._start.getTime());
        // Same or virtually identical start time (< 60s) -> Duplicate!
        if (diffStartMs < 60000) {
          // Merge metadata into prevEv if prevEv is missing language
          if (!getLocalized(prevEv.titles, currentLanguage) && getLocalized(e.titles, currentLanguage)) {
            prevEv.titles = { ...prevEv.titles, ...e.titles };
          }
          continue; // Discard duplicate!
        }

        // If this event starts before prevEv ends (overlap)
        if (e._start < prevEv._end) {
          // If prevEv is a huge placeholder (> 4x duration of e)
          if ((prevEv._end - prevEv._start) > 4 * (e._end - e._start)) {
            validEvents.pop(); // Remove the placeholder
          } else {
            // Normal transition: clamp prevEv end time to when e starts
            prevEv._end = e._start;
          }
        }
      }
      validEvents.push(e);
      prevEv = e;
    }

    const rowFragment = document.createDocumentFragment();

    for (let i = 0; i < validEvents.length; i++) {
      const e = validEvents[i];
      if (e._end <= e._start) continue;

      const effectiveStart = e._start < startTime ? startTime : e._start;
      const effectiveEnd = e._end > endTime ? endTime : e._end;
      if (effectiveEnd <= effectiveStart) continue;

      const offsetMinutes = Math.max(0, (effectiveStart - startTime) / 60000);
      const durationMinutes = (effectiveEnd - effectiveStart) / 60000;

      const leftPx = offsetMinutes * PIXELS_PER_MINUTE;
      const widthPx = Math.max(14, durationMinutes * PIXELS_PER_MINUTE - 4);

      const block = document.createElement('div');
      const isNow = nowMs >= e._start.getTime() && nowMs < e._end.getTime();

      const matchedRec = recMap.get(`ev_${c.id}_${e.event_id || e.id}`) ||
                         recMap.get(`time_${c.id}_${e._start.getTime()}`);

      let recBadge = '';
      let blockClass = 'epg-event-block';
      if (isNow) blockClass += ' active-now';
      if (matchedRec) {
        if (matchedRec.status === 'recording') {
          recBadge = '<span class="epg-block-rec-badge">🔴 REC</span>';
          blockClass += ' epg-block-recording';
        } else if (matchedRec.status === 'scheduled') {
          recBadge = '<span class="epg-block-sched-badge">⏱️</span>';
          blockClass += ' epg-block-scheduled';
        }
      }

      block.className = blockClass;
      block.style.left = `${leftPx}px`;
      block.style.width = `${widthPx}px`;

      const title = getLocalized(e.titles, currentLanguage) || 'Broadcast';
      const startStr = formatEPGTime(e._start);
      const endStr = formatEPGTime(e._end);

      block._eventData = e;
      block.dataset.eventId = e.id || e.event_id;
      block.dataset.title = title;
      block.title = `${title} (${startStr} - ${endStr})${matchedRec ? ' [' + matchedRec.status.toUpperCase() + ']' : ''}`;

      block.innerHTML = `
        <div class="epg-block-title">${recBadge}${escapeHtml(title)}</div>
        <div class="epg-block-time">${startStr} - ${endStr}</div>
      `;
      block.onclick = (ev) => {
        ev.stopPropagation();
        if (window._epgIsDragging) return;
        showEPGModal(e, c.name);
      };

      rowFragment.appendChild(block);
    }

    row.appendChild(rowFragment);
    fragment.appendChild(row);
  }

  container.innerHTML = '';
  container.appendChild(fragment);

  // Ensure NOW marker line height covers all channel rows
  updateNowMarkerPosition();

  // Re-apply in-grid search highlights if search input is active
  const searchInput = document.getElementById('epg-search-input');
  if (searchInput && searchInput.value.trim().length >= 2) {
    highlightInGridEvents(searchInput.value.trim());
  }
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
  const now = new Date();
  const minutesSinceStart = (now.getTime() - timelineStartTime.getTime()) / (60 * 1000);
  const targetPx = minutesSinceStart * PIXELS_PER_MINUTE - (gridScroll.clientWidth / 2);

  isProgrammaticDayScroll = true;
  gridScroll.scrollTo({ left: Math.max(0, targetPx), behavior: 'smooth' });

  // Update active day button to 'today'
  const dayButtons = document.querySelectorAll('.day-btn');
  const todayBtn = Array.from(dayButtons).find(btn => btn._dayDate && isSameDay(btn._dayDate, now)) || dayButtons[0];
  if (todayBtn) {
    dayButtons.forEach(b => b.classList.remove('active'));
    todayBtn.classList.add('active');
    scrollDayButtonIntoView(todayBtn, true);
  }

  clearTimeout(dayScrollTimeout);
  dayScrollTimeout = setTimeout(() => {
    isProgrammaticDayScroll = false;
  }, 600);
}

// ==========================================================
// EPG PROGRAM SEARCH & TIMELINE HIGHLIGHTING
// ==========================================================
function setupEPGSearch() {
  const input = document.getElementById('epg-search-input');
  const clearBtn = document.getElementById('btn-epg-search-clear');
  const dropdown = document.getElementById('epg-search-dropdown');
  if (!input || !dropdown) return;

  let debounceTimer = null;

  const performSearch = async (query) => {
    query = query.trim();
    if (query.length < 2) {
      dropdown.classList.add('hidden');
      if (clearBtn) clearBtn.classList.add('hidden');
      clearInGridSearchHighlights();
      return;
    }

    if (clearBtn) clearBtn.classList.remove('hidden');

    try {
      const res = await fetch(`/api/epg/search?q=${encodeURIComponent(query)}`);
      const results = await res.json() || [];
      renderEPGSearchResults(results, query);
      highlightInGridEvents(query);
    } catch (err) {
      console.error('[EPG Search] Error:', err);
    }
  };

  input.addEventListener('input', (e) => {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => {
      performSearch(e.target.value);
    }, 220);
  });

  input.addEventListener('focus', () => {
    if (input.value.trim().length >= 2) {
      performSearch(input.value);
    }
  });

  if (clearBtn) {
    clearBtn.addEventListener('click', () => {
      input.value = '';
      dropdown.classList.add('hidden');
      clearBtn.classList.add('hidden');
      clearInGridSearchHighlights();
      input.focus();
    });
  }

  // Close dropdown on Escape key or clicking outside
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !dropdown.classList.contains('hidden')) {
      dropdown.classList.add('hidden');
    }
  });

  document.addEventListener('click', (e) => {
    if (!e.target.closest('.epg-search-container')) {
      dropdown.classList.add('hidden');
    }
  });
}

function renderEPGSearchResults(results, query) {
  const dropdown = document.getElementById('epg-search-dropdown');
  if (!dropdown) return;

  if (results.length === 0) {
    dropdown.innerHTML = `
      <div class="epg-search-dropdown-header"><span>Search Results</span></div>
      <div class="epg-search-dropdown-empty">No programs found matching "<strong>${escapeHtml(query)}</strong>"</div>
    `;
    dropdown.classList.remove('hidden');
    return;
  }

  let html = `
    <div class="epg-search-dropdown-header">
      <span>Programs matching "${escapeHtml(query)}"</span>
      <span>${results.length} found</span>
    </div>
  `;

  const nowMs = Date.now();

  results.forEach(item => {
    const title = getLocalized(item.titles, currentLanguage) || 'Program';
    const startDate = new Date(item.start_time);
    const endDate = new Date(item.end_time);
    const isLive = nowMs >= startDate.getTime() && nowMs < endDate.getTime();

    const timeStr = formatSearchDate(startDate, endDate);

    const logoHtml = item.channel_logo ? 
      `<img src="${escapeHtml(item.channel_logo)}" alt="" style="width:20px; height:20px; object-fit:contain; border-radius:3px; margin-bottom:2px;">` : '';

    const highlightedTitle = highlightMatchedTokens(title, query);

    html += `
      <div class="epg-search-result-item" data-channel-id="${item.channel_id}" data-start-time="${item.start_time}" data-event-id="${item.id}">
        <div class="epg-search-ch-badge">
          ${logoHtml}
          <span class="epg-search-ch-num">${item.channel_number}</span>
          <span class="epg-search-ch-name" title="${escapeHtml(item.channel_name)}">${escapeHtml(item.channel_name)}</span>
        </div>
        <div class="epg-search-info">
          <div class="epg-search-prog-title">
            <span>${highlightedTitle}</span>
            ${isLive ? '<span class="epg-search-live-tag">LIVE NOW</span>' : ''}
          </div>
          <div class="epg-search-prog-time">📅 ${timeStr}</div>
        </div>
        <div class="epg-search-actions">
          ${isLive ? `<button type="button" class="btn btn-sm btn-primary epg-search-action-watch" title="Watch live in player">▶ Watch</button>` : ''}
          <button type="button" class="btn btn-sm btn-secondary epg-search-action-jump" title="Show in TV Guide">📅 In Guide</button>
        </div>
      </div>
    `;
  });

  dropdown.innerHTML = html;
  dropdown.classList.remove('hidden');

  // Attach interactive click handlers
  dropdown.querySelectorAll('.epg-search-result-item').forEach(el => {
    const chId = Number(el.dataset.channelId);
    const startTime = el.dataset.startTime;
    const eventId = Number(el.dataset.eventId);

    const btnWatch = el.querySelector('.epg-search-action-watch');
    if (btnWatch) {
      btnWatch.onclick = (e) => {
        e.stopPropagation();
        dropdown.classList.add('hidden');
        const ch = channels.find(c => c.id === chId);
        if (ch) {
          zapToChannel(ch);
          window.location.hash = '#/tv';
        }
      };
    }

    const btnJump = el.querySelector('.epg-search-action-jump');
    if (btnJump) {
      btnJump.onclick = (e) => {
        e.stopPropagation();
        dropdown.classList.add('hidden');
        jumpToEPGEvent(chId, startTime, eventId);
      };
    }

    el.onclick = () => {
      dropdown.classList.add('hidden');
      jumpToEPGEvent(chId, startTime, eventId);
    };
  });
}

function formatSearchDate(start, end) {
  const now = new Date();
  const isToday = start.toDateString() === now.toDateString();
  const tmrw = new Date(now.getTime() + 86400000);
  const isTomorrow = start.toDateString() === tmrw.toDateString();

  let dayLabel = start.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
  if (isToday) dayLabel = 'Today';
  else if (isTomorrow) dayLabel = 'Tomorrow';

  const sTime = start.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const eTime = end.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return `${dayLabel}, ${sTime} – ${eTime}`;
}

async function jumpToEPGEvent(chId, startTimeIso, eventId) {
  const startTime = new Date(startTimeIso);
  const gridScroll = document.getElementById('epg-grid-scroll');
  if (!gridScroll || !timelineStartTime) return;

  // Sync active day button
  const dayButtons = document.querySelectorAll('.day-btn');
  const eventDateStr = startTime.toDateString();
  dayButtons.forEach(btn => {
    if (btn._dayDate && btn._dayDate.toDateString() === eventDateStr) {
      document.querySelectorAll('.day-btn').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      scrollDayButtonIntoView(btn, true);
    }
  });

  isProgrammaticDayScroll = true;
  clearTimeout(dayScrollTimeout);
  dayScrollTimeout = setTimeout(() => {
    isProgrammaticDayScroll = false;
  }, 600);

  // Calculate horizontal offset
  const minutesSinceStart = (startTime.getTime() - timelineStartTime.getTime()) / (60 * 1000);
  const chColWidth = parseInt(getComputedStyle(document.documentElement).getPropertyValue('--ch-col-width')) || 140;
  const targetLeft = Math.max(0, minutesSinceStart * PIXELS_PER_MINUTE - chColWidth - 10);

  // Vertical scroll to channel row
  const row = document.querySelector(`.epg-channel-row-track[data-channel-id="${chId}"]`);
  let targetTop = gridScroll.scrollTop;
  if (row) {
    targetTop = row.offsetTop - 70;
  }

  gridScroll.scrollTo({ left: targetLeft, top: targetTop, behavior: 'smooth' });

  // Highlight block with pulse animation
  setTimeout(() => {
    const blocks = document.querySelectorAll(`.epg-channel-row-track[data-channel-id="${chId}"] .epg-event-block`);
    blocks.forEach(b => {
      const bStart = b._eventData ? new Date(b._eventData.start_time).getTime() : 0;
      if (Math.abs(bStart - startTime.getTime()) < 60000 || (eventId && b.dataset.eventId == eventId)) {
        b.classList.add('pulse-highlight');
        setTimeout(() => b.classList.remove('pulse-highlight'), 4500);
      }
    });
  }, 400);
}

function highlightMatchedTokens(text, query) {
  if (!text) return '';
  const escaped = escapeHtml(text);
  if (!query) return escaped;
  const tokens = normalizeSearchText(query).split(/\s+/).filter(t => t.length >= 2);
  if (tokens.length === 0) return escaped;

  let result = escaped;
  tokens.forEach(tok => {
    const pattern = tok.split('').map(char => {
      const greekMap = {
        'α': '[αάΑΆ]',
        'ε': '[εέΕΈ]',
        'η': '[ηήΗΉ]',
        'ι': '[ιίΐϊΙΊΪ]',
        'ο': '[οόΟΌ]',
        'υ': '[υύΰϋΥΎΫ]',
        'ω': '[ωώΩΏ]',
        'σ': '[σςΣ]',
        'a': '[aáàâäAÁÀÂÄ]',
        'e': '[eéèêëEÉÈÊË]',
        'i': '[iíìîïIÍÌÎÏ]',
        'o': '[oóòôöOÓÒÔÖ]',
        'u': '[uúùûüUÚÙÛÜ]'
      };
      return greekMap[char] || char.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    }).join('');

    try {
      const rx = new RegExp(`(${pattern})`, 'gi');
      result = result.replace(rx, '<mark class="epg-search-hl">$1</mark>');
    } catch (_) {}
  });
  return result;
}

function highlightInGridEvents(query) {
  const qNorm = normalizeSearchText(query);
  if (!qNorm) {
    clearInGridSearchHighlights();
    return;
  }
  const tokens = qNorm.split(/\s+/).filter(Boolean);
  document.querySelectorAll('.epg-event-block').forEach(b => {
    const text = normalizeSearchText(b.textContent || '');
    const title = normalizeSearchText(b.dataset.title || b.title || '');
    let desc = '';
    if (b._eventData && b._eventData.descriptions) {
      desc = normalizeSearchText(Object.values(b._eventData.descriptions).join(' '));
    }
    const combined = `${text} ${title} ${desc}`;
    const matches = tokens.every(tok => combined.includes(tok));
    b.classList.toggle('search-matched', matches);
    b.classList.toggle('search-dimmed', !matches);
  });
}

function clearInGridSearchHighlights() {
  document.querySelectorAll('.epg-event-block').forEach(b => {
    b.classList.remove('search-matched', 'search-dimmed');
  });
}

// Show EPG Event Details Modal
window.showEPGModal = function(event, channelName) {
  const modal = document.getElementById('epg-modal');
  const videoWrapper = document.getElementById('video-wrapper');
  const isFullscreen = !!document.fullscreenElement;
  const modalTarget = isFullscreen ? (document.fullscreenElement === videoWrapper ? videoWrapper : document.fullscreenElement) : document.body;
  if (modal && modal.parentElement !== modalTarget) {
    modalTarget.appendChild(modal);
  }

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
  if (modal) {
    modal.classList.add('hidden');
    if (!document.fullscreenElement && modal.parentElement !== document.body) {
      document.body.appendChild(modal);
    }
  }
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
    const cachedLang = localStorage.getItem('dvb_epg_language');
    if (cachedLang && availableLanguages.includes(cachedLang)) {
      currentLanguage = cachedLang;
      langSelect.value = currentLanguage;
    } else if (cfg.preferred_language && availableLanguages.includes(cfg.preferred_language)) {
      currentLanguage = cfg.preferred_language;
      langSelect.value = currentLanguage;
    }
    if (cfg.transcoder_mode) {
      document.getElementById('cfg-transcoder-mode').value = cfg.transcoder_mode;
    }
    if (cfg.video_bitrate) {
      document.getElementById('cfg-video-bitrate').value = cfg.video_bitrate;
    }
    if (cfg.rec_pre_padding !== undefined) {
      const el = document.getElementById('cfg-rec-pre-padding');
      if (el) el.value = cfg.rec_pre_padding;
    }
    if (cfg.rec_post_padding !== undefined) {
      const el = document.getElementById('cfg-rec-post-padding');
      if (el) el.value = cfg.rec_post_padding;
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
      updateHeaderTunerBadge();
      updateInstantRecordButton();
  updateTimeshiftUI();
    }

    document.getElementById('cfg-detected-hw').value = data.hardware ? data.hardware.toUpperCase() : 'CPU';

    const container = document.getElementById('tuner-list-container');
    container.innerHTML = '';

    data.tuners.forEach(t => {
      const isChecked = t.enabled ? 'checked' : '';
      let statusBadge = t.status === 'active' ? 'badge-active' : 'badge-idle';
      let statusText = t.status.toUpperCase();

      if (t.active_recording) {
        statusBadge = 'badge-recording';
        statusText = '🔴 RECORDING';
      }

      let recordingBanner = '';
      if (t.active_recording) {
        recordingBanner = `
          <div class="viewer-row recording-row" style="background: rgba(239, 68, 68, 0.15); border: 1px solid rgba(239, 68, 68, 0.45); border-radius: 6px; padding: 0.5rem 0.75rem; color: #fca5a5; display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.6rem;">
            <div>
              <div style="font-weight: 700; color: #f87171; display: flex; align-items: center; gap: 0.4rem;">
                <span class="live-dot pulse"></span> DVR RECORDER ACTIVE
              </div>
              <div style="color: #fff; font-size: 0.85rem; margin-top: 0.2rem;">
                "${t.active_recording.title}" on <strong>${t.active_recording.channel_name}</strong>
              </div>
            </div>
            <button class="btn btn-danger" style="padding: 0.25rem 0.6rem; font-size: 0.78rem; font-weight: 600;" onclick="stopRecordingFromTuner(${t.active_recording.recording_id})">⏹ Stop Recording</button>
          </div>
        `;
      }

      let viewersHtml = '';
      if (t.viewers && t.viewers.length > 0) {
        viewersHtml = t.viewers.map(v => `
          <div class="viewer-row">
            <span>IP: <strong class="viewer-ip">${v.ip}</strong> (watching ${v.channel_name})</span>
            <button class="btn btn-danger" style="padding:0.15rem 0.4rem; font-size:0.75rem;" onclick="kickTuner(${t.id})">Force Kick</button>
          </div>
        `).join('');
      } else {
        if (t.active_recording) {
          viewersHtml = '<span style="color:var(--text-muted); font-size:0.85rem;">No live streaming viewers (Tuner locked by recorder)</span>';
        } else {
          viewersHtml = '<span style="color:var(--text-muted); font-size:0.85rem;">No active viewers (Tuner sleeping)</span>';
        }
      }

      const signalHtml = t.signal && (t.signal.has_lock || t.signal.signal_quality > 0) ? `
        <div class="tuner-signal-meter">
          📶 Signal: <strong>${t.signal.signal_strength}%</strong> | Quality: <strong>${t.signal.signal_quality}%</strong>${t.signal.snr_db ? ' (SNR: ' + t.signal.snr_db.toFixed(1) + ' dB)' : ''}${t.signal.has_lock ? ' <span class="badge" style="background:var(--success); font-size:0.7rem; padding:1px 4px;">Locked</span>' : ''}
        </div>
      ` : '';

      container.innerHTML += `
        <div class="tuner-item ${t.active_recording ? 'tuner-recording-active' : ''}">
          <div class="tuner-item-top">
            <div class="tuner-left-info">
              <div>
                <span class="tuner-name">${t.name}</span>
                <div class="tuner-sub">${t.frontend_path || 'Virtual Adapter'}${t.current_frequency ? ' • ' + (t.current_frequency / 1e6).toFixed(1) + ' MHz' : ''}${t.active_recording ? ' • ⏺ Recording: ' + t.active_recording.channel_name : ''}</div>
                ${signalHtml}
              </div>
            </div>
            <div class="tuner-actions">
              <span class="tuner-badge ${statusBadge}">${statusText}</span>
              <label class="switch" title="Enable/Disable Tuner">
                <input type="checkbox" ${isChecked} onchange="toggleTuner(${t.id}, this.checked)">
                <span class="slider"></span>
              </label>
            </div>
          </div>
          <div class="viewers-box">
            ${recordingBanner}
            ${viewersHtml}
          </div>
        </div>
      `;
    });
  } catch (e) {
    console.error('Failed to fetch tuner status:', e);
  }
}

function updateHeaderTunerBadge() {
  if (!tunerBadge) return;
  const hasRecording = currentTuners && currentTuners.some(t => t.active_recording);
  const activeTuner = currentTuners && currentTuners.find(t => t.status === 'active');

  if (player && activeChannel) {
    if (hasRecording) {
      tunerBadge.className = 'tuner-badge badge-recording';
      tunerBadge.textContent = 'Active + ⏺ Recording';
    } else {
      tunerBadge.className = 'tuner-badge badge-active';
      tunerBadge.textContent = `Live: ${activeChannel.name}`;
    }
  } else if (hasRecording) {
    tunerBadge.className = 'tuner-badge badge-recording';
    tunerBadge.textContent = '⏺ Recording Active';
  } else if (activeTuner) {
    tunerBadge.className = 'tuner-badge badge-active';
    tunerBadge.textContent = `Tuner ${activeTuner.id}: Active`;
  } else {
    tunerBadge.className = 'tuner-badge badge-idle';
    tunerBadge.textContent = 'Idle (0% CPU)';
  }
}

window.stopRecordingFromTuner = async function(recId) {
  const confirmed = await showConfirm('Are you sure you want to stop this recording?', 'Stop Recording', 'Stop Recording', true);
  if (!confirmed) return;
  try {
    await fetch(`/api/recordings/${recId}/stop`, { method: 'POST' });
    showToast('⏺ Recording stopped', 'success');
    refreshTunerStatus();
    loadRecordings();
    updateInstantRecordButton();
  updateTimeshiftUI();
    updateHeaderTunerBadge();
  } catch (err) {
    console.error('Failed to stop recording:', err);
  }
};

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
  const confirmed = await showConfirm(`Are you sure you want to force-kick all viewers on Tuner ${tunerId}?`, 'Force Release Tuner', 'Kick Viewers', true);
  if (!confirmed) return;
  await fetch(`/api/tuners/${tunerId}/force-release`, { method: 'POST' });
  refreshTunerStatus();
};

// Channel Scanner
let activeScanData = {
  allFound: [],
  newChannels: [],
  updatedChannels: [],
  missingChannels: [],
  filter: 'all',
  search: ''
};

function renderScanResultsCards() {
  const grid = document.getElementById('scan-channels-grid');
  if (!grid) return;
  grid.innerHTML = '';

  const { allFound, newChannels, missingChannels, filter, search } = activeScanData;
  let targetList = [];

  if (filter === 'new') {
    targetList = newChannels;
  } else if (filter === 'existing') {
    targetList = activeScanData.updatedChannels;
  } else if (filter === 'missing') {
    targetList = missingChannels;
  } else {
    targetList = allFound;
  }

  if (search.trim()) {
    const sNorm = normalizeSearchText(search);
    const sTokens = sNorm.split(/\s+/).filter(Boolean);
    targetList = targetList.filter(c => {
      const cNorm = normalizeSearchText(`${c.name} ${c.number}`);
      return sTokens.every(tok => cNorm.includes(tok));
    });
  }

  if (targetList.length === 0) {
    grid.innerHTML = `<div style="grid-column: 1/-1; padding: 2rem; text-align: center; color: var(--text-muted); font-size: 0.95rem;">
      ${filter === 'new' ? '✨ No new channels were discovered in this sweep (all found channels already exist in your channel list).' : 
        filter === 'missing' ? '🎉 Great news! No existing channels are missing.' :
        'No channels match your current filter or search query.'}
    </div>`;
    return;
  }

  targetList.forEach(c => {
    const isNew = newChannels.some(nc => nc.id === c.id || (nc.service_id === c.service_id && nc.frequency === c.frequency));
    const card = document.createElement('div');
    card.className = 'scan-channel-card' + (isNew ? ' is-new' : '');
    card.innerHTML = `
      <div class="scan-ch-left">
        <div class="scan-ch-num">${c.number}</div>
        <div class="scan-ch-details">
          <div class="scan-ch-name" title="${escapeHtml(c.name)}">${escapeHtml(c.name)}</div>
          <div class="scan-ch-meta">
            <span>📡 ${(c.frequency / 1e6).toFixed(1)} MHz</span>
            <span>•</span>
            <span>${c.delivery_system || 'DVB-T'}</span>
            <span>•</span>
            <span>SID ${c.service_id}</span>
          </div>
        </div>
      </div>
      <div class="scan-ch-badges">
        ${isNew ? '<span class="badge-new">✨ NEW</span>' : ''}
        <span class="badge-tag">${c.is_radio ? '📻 Radio' : '📺 TV'}</span>
        ${(c.name && c.name.toUpperCase().includes('HD')) ? '<span class="badge-tag" style="color:#60a5fa;border-color:rgba(59,130,246,0.3);">HD</span>' : ''}
      </div>
    `;
    grid.appendChild(card);
  });
}

function setupScanFilterHandlers() {
  const statPills = document.querySelectorAll('.scan-stats-pills .stat-pill');
  statPills.forEach(pill => {
    pill.onclick = () => {
      statPills.forEach(p => p.classList.remove('active'));
      pill.classList.add('active');
      activeScanData.filter = pill.dataset.filter || 'all';
      renderScanResultsCards();
    };
  });

  const searchInput = document.getElementById('scan-results-search');
  if (searchInput) {
    searchInput.oninput = (e) => {
      activeScanData.search = e.target.value;
      renderScanResultsCards();
    };
  }
}

setupScanFilterHandlers();

document.getElementById('btn-start-scan').onclick = async () => {
  const btn = document.getElementById('btn-start-scan');
  const btnText = document.getElementById('scan-btn-text');
  const spinner = document.getElementById('scan-spinner');
  const statusPill = document.getElementById('scan-status-pill');
  const status = document.getElementById('scan-status');
  const progressBox = document.getElementById('scan-progress-box');
  const progressFill = document.getElementById('scan-progress-fill');
  const pctLabel = document.getElementById('scan-pct-label');
  const foundCount = document.getElementById('scan-found-count');
  const freqLabel = document.getElementById('scan-freq-label');
  const liveTicker = document.getElementById('scan-live-ticker');
  const resultsBox = document.getElementById('scan-results-box');

  // Disable button and show active scanner state
  btn.disabled = true;
  if (btnText) btnText.textContent = 'Scanning frequencies...';
  if (spinner) spinner.classList.remove('hidden');
  if (statusPill) statusPill.classList.remove('hidden');
  if (status) status.textContent = 'Scanning frequencies...';

  if (progressBox) progressBox.classList.remove('hidden');
  if (progressFill) progressFill.style.width = '0%';
  if (pctLabel) pctLabel.textContent = '0%';
  if (foundCount) foundCount.textContent = '0 found';
  if (freqLabel) freqLabel.textContent = 'Starting frequency sweep...';
  if (liveTicker) liveTicker.innerHTML = '<div class="ticker-placeholder">Listening for multiplex transport streams...</div>';
  if (resultsBox) resultsBox.classList.add('hidden');

  try {
    const res = await fetch('/api/scan', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ adapter_id: 0 })
    });
    scanDiff = await res.json();
    if (!res.ok) {
      if (status) status.textContent = 'Scan failed: ' + (scanDiff.error || 'Server error');
      showToast('⚠️ Scan failed: ' + (scanDiff.error || 'Server error'), 'error');
      return;
    }

    if (status) status.textContent = 'Scan complete!';
    if (progressFill) progressFill.style.width = '100%';
    if (pctLabel) pctLabel.textContent = '100%';

    const newChannels = scanDiff.new_channels || [];
    const updatedChannels = scanDiff.updated_channels || [];
    const missingChannels = scanDiff.missing_channels || [];
    const allFound = [...newChannels, ...updatedChannels];
    allFound.sort((a, b) => a.number - b.number);
    const discovered = scanDiff.discovered_count || allFound.length;

    activeScanData.allFound = allFound;
    activeScanData.newChannels = newChannels;
    activeScanData.updatedChannels = updatedChannels;
    activeScanData.missingChannels = missingChannels;
    activeScanData.filter = 'all';
    activeScanData.search = '';

    // Instant toast notification with full summary
    showToast(`✅ Channel scan complete! Found ${discovered} channels (${newChannels.length} new)`, 'success');

    // Update pill counters
    const statDisc = document.getElementById('stat-discovered');
    if (statDisc) statDisc.textContent = `${discovered} Discovered`;
    const statNew = document.getElementById('stat-new');
    if (statNew) statNew.textContent = `${newChannels.length} New`;
    const statUp = document.getElementById('stat-updated');
    if (statUp) statUp.textContent = `${updatedChannels.length} Existing`;
    const statMiss = document.getElementById('stat-missing');
    if (statMiss) statMiss.textContent = `${missingChannels.length} Missing`;

    // Subtitle
    const subTitle = document.getElementById('scan-summary-subtitle');
    if (subTitle) {
      subTitle.textContent = `Sweep finished: ${discovered} broadcast channels discovered (${newChannels.length} new), ${missingChannels.length} missing.`;
    }

    // Reset search input
    const sInput = document.getElementById('scan-results-search');
    if (sInput) sInput.value = '';

    // Reset stat pills state to 'all'
    const statPills = document.querySelectorAll('.scan-stats-pills .stat-pill');
    statPills.forEach(p => p.classList.toggle('active', p.dataset.filter === 'all'));

    renderScanResultsCards();

    // Render Missing Channels
    const missingSection = document.getElementById('missing-channels-section');
    const missingList = document.getElementById('missing-channels-list');
    if (missingSection && missingList) {
      missingList.innerHTML = '';
      if (missingChannels.length > 0) {
        missingSection.classList.remove('hidden');
        missingChannels.forEach(c => {
          const tag = document.createElement('div');
          tag.className = 'missing-ch-tag';
          tag.innerHTML = `<strong>${c.number}:</strong> ${escapeHtml(c.name)}`;
          missingList.appendChild(tag);
        });
      } else {
        missingSection.classList.add('hidden');
      }
    }

    if (resultsBox) resultsBox.classList.remove('hidden');

    await loadChannels();
  } catch (err) {
    if (status) status.textContent = 'Scan failed: ' + err.message;
    showToast('⚠️ Scan failed: ' + err.message, 'error');
  } finally {
    btn.disabled = false;
    if (btnText) btnText.textContent = 'Start Channel Scan';
    if (spinner) spinner.classList.add('hidden');
  }
};

// Prune Missing Channels
document.getElementById('btn-prune-missing').onclick = async () => {
  if (!scanDiff || !scanDiff.missing_channels || !scanDiff.missing_channels.length) return;
  const ids = scanDiff.missing_channels.map(c => c.id);

  const confirmed = await showConfirm(`Are you sure you want to delete ${ids.length} missing channels?`, 'Delete Missing Channels', 'Delete Channels', true);
  if (!confirmed) return;

  try {
    await fetch('/api/scan/prune', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ channel_ids: ids })
    });

    scanDiff.missing_channels = [];
    activeScanData.missingChannels = [];
    document.getElementById('missing-channels-section').classList.add('hidden');
    const statMiss = document.getElementById('stat-missing');
    if (statMiss) statMiss.textContent = '0 Missing';
    renderScanResultsCards();
    showToast(`🗑️ Successfully deleted ${ids.length} missing channels`, 'info');
    await loadChannels();
  } catch (err) {
    showToast('⚠️ Failed to delete missing channels: ' + err.message, 'error');
  }
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
      showToast('Logo updated successfully!', 'success');
    } else {
      showToast('⚠️ Failed to upload logo.', 'error');
    }
  } catch (err) {
    console.error('Logo upload error:', err);
  }
};

window.deleteChannel = async function(channelId) {
  const confirmed = await showConfirm('Are you sure you want to delete this channel?', 'Delete Channel', 'Delete', true);
  if (!confirmed) return;
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
      rec_pre_padding: parseInt(document.getElementById('cfg-rec-pre-padding').value, 10),
      rec_post_padding: parseInt(document.getElementById('cfg-rec-post-padding').value, 10),
    };
    await fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(updates),
    });
    showToast('Settings updated successfully!', 'success');
  };
}

// ==========================================================
// AUDIO & SUBTITLE SELECTION (In-Sync Muxing & Burned-in Subtitles)
// ==========================================================
let currentAudioTrack = 0;
let currentSubTrack = -1; // -1 = off
let currentSubOffsetMs = parseInt(localStorage.getItem('dvb_sub_offset_ms') || '0', 10);
if (isNaN(currentSubOffsetMs)) currentSubOffsetMs = 0;
let availableChannelTracks = { audio_tracks: [], subtitle_tracks: [] };

function adjustSubtitleOffset(deltaMs) {
  currentSubOffsetMs += deltaMs;
  if (currentSubOffsetMs < -5000) currentSubOffsetMs = -5000;
  if (currentSubOffsetMs > 5000) currentSubOffsetMs = 5000;
  localStorage.setItem('dvb_sub_offset_ms', currentSubOffsetMs);
  const sec = (currentSubOffsetMs / 1000).toFixed(1);
  showToast(`⏱️ Subtitle Sync: ${sec > 0 ? '+' : ''}${sec}s`, 'info');
  renderTrackMenus();
  if (currentSubTrack >= 0 && activeChannel) {
    switchStreamTracks(activeChannel);
  }
}
window.adjustSubtitleOffset = adjustSubtitleOffset;

function resetSubtitleOffset() {
  currentSubOffsetMs = 0;
  localStorage.setItem('dvb_sub_offset_ms', currentSubOffsetMs);
  showToast('⏱️ Subtitle Sync reset to 0.0s', 'info');
  renderTrackMenus();
  if (currentSubTrack >= 0 && activeChannel) {
    switchStreamTracks(activeChannel);
  }
}
window.resetSubtitleOffset = resetSubtitleOffset;

async function fetchAndRenderChannelTracks(channelId) {
  try {
    const res = await fetch(`/api/channels/${channelId}/tracks`);
    if (res.ok) {
      availableChannelTracks = await res.json();
      renderTrackMenus();
      updateInteractiveFeaturesUI(activeChannel);
    } else {
      availableChannelTracks = { audio_tracks: [], subtitle_tracks: [] };
      renderTrackMenus();
      updateInteractiveFeaturesUI(activeChannel);
    }
  } catch (e) {
    console.warn('[Tracks] Failed to fetch channel tracks:', e);
    availableChannelTracks = { audio_tracks: [], subtitle_tracks: [] };
    renderTrackMenus();
    updateInteractiveFeaturesUI(activeChannel);
  }
}

function renderTrackMenus() {
  const audioWrap = document.getElementById('audio-dropdown-wrap');
  const subWrap = document.getElementById('sub-dropdown-wrap');
  const audioMenu = document.getElementById('audio-tracks-menu');
  const subMenu = document.getElementById('sub-tracks-menu');
  const audioBtn = document.getElementById('btn-audio-select');
  const subBtn = document.getElementById('btn-sub-select');

  const audioTracks = availableChannelTracks.audio_tracks || [];
  const subTracks = availableChannelTracks.subtitle_tracks || [];

  // Hide audio dropdown if there is only 1 or 0 audio tracks (no alternate track can be chosen)
  if (audioWrap) {
    if (audioTracks.length > 1) {
      audioWrap.classList.remove('hidden');
    } else {
      audioWrap.classList.add('hidden');
      if (audioMenu) audioMenu.classList.add('hidden');
    }
  }

  // Show subtitle dropdown for all non-radio television channels
  const isRadio = activeChannel && activeChannel.is_radio;
  if (subWrap) {
    if (!isRadio) {
      subWrap.classList.remove('hidden');
    } else {
      subWrap.classList.add('hidden');
      if (subMenu) subMenu.classList.add('hidden');
    }
  }

  // Render Audio Menu
  if (audioMenu) {
    audioMenu.innerHTML = '';
    if (audioTracks.length === 0) {
      audioMenu.innerHTML = '<div style="padding: 0.4rem 0.6rem; font-size: 0.78rem; color: var(--text-muted);">Default Audio (Stereo)</div>';
      if (audioBtn) {
        audioBtn.textContent = '🎧 Audio';
        audioBtn.classList.remove('has-active');
      }
    } else {
      audioTracks.forEach(t => {
        const item = document.createElement('button');
        item.className = `track-item ${currentAudioTrack === t.index ? 'active' : ''}`;
        const desc = t.description || t.title || (t.language ? t.language.toUpperCase() : `Audio Track ${t.index + 1}`);
        item.innerHTML = `<span>${desc}</span><span class="track-item-tag">${t.codec ? t.codec.toUpperCase() : ''}</span>`;
        item.onclick = (e) => {
          e.stopPropagation();
          currentAudioTrack = t.index;
          audioMenu.classList.add('hidden');
          renderTrackMenus();
          switchStreamTracks(activeChannel);
        };
        audioMenu.appendChild(item);
      });
      if (audioBtn) {
        const activeTrack = audioTracks.find(t => t.index === currentAudioTrack) || audioTracks[0];
        const activeDesc = activeTrack ? (activeTrack.language ? activeTrack.language.toUpperCase() : `Audio ${activeTrack.index + 1}`) : 'Audio';
        audioBtn.textContent = `🎧 ${activeDesc}`;
        audioBtn.classList.add('has-active');
      }
    }
  }

  // Render Subtitles Menu
  if (subMenu) {
    subMenu.innerHTML = '';

    // If channel has no subtitles broadcast, force currentSubTrack to off
    if (currentSubTrack >= 0 && subTracks.length === 0) {
      currentSubTrack = -1;
    }

    // Always include "Off" option
    const offItem = document.createElement('button');
    offItem.className = `track-item ${currentSubTrack === -1 ? 'active' : ''}`;
    offItem.innerHTML = `<span>Off (No Subtitles)</span>`;
    offItem.onclick = (e) => {
      e.stopPropagation();
      currentSubTrack = -1;
      subMenu.classList.add('hidden');
      renderTrackMenus();
      switchStreamTracks(activeChannel);
    };
    subMenu.appendChild(offItem);

    if (subTracks.length > 0) {
      subTracks.forEach(s => {
        const item = document.createElement('button');
        item.className = `track-item ${currentSubTrack === s.index ? 'active' : ''}`;
        const desc = s.title || s.description || (s.language && s.language !== 'und' ? s.language.toUpperCase() : `Subtitle Track ${s.index + 1}`);
        item.innerHTML = `<span>${desc}</span><span class="track-item-tag">Burned In</span>`;
        item.onclick = (e) => {
          e.stopPropagation();
          currentSubTrack = s.index;
          subMenu.classList.add('hidden');
          renderTrackMenus();
          switchStreamTracks(activeChannel);
        };
        subMenu.appendChild(item);
      });
    } else {
      const noSubItem = document.createElement('div');
      noSubItem.className = 'track-item disabled';
      noSubItem.style.opacity = '0.65';
      noSubItem.style.cursor = 'pointer';
      noSubItem.style.pointerEvents = 'auto';
      noSubItem.innerHTML = `<span>No Subtitles Broadcast</span><span class="track-item-tag">N/A</span>`;
      noSubItem.onclick = (e) => {
        e.stopPropagation();
        showToast('ℹ️ No subtitles are broadcast on this channel', 'info');
      };
      subMenu.appendChild(noSubItem);
    }

    // Subtitle Sync / Delay Controls (when subtitles are active)
    if (currentSubTrack >= 0) {
      const syncDivider = document.createElement('div');
      syncDivider.className = 'track-menu-divider';
      subMenu.appendChild(syncDivider);

      const syncHeader = document.createElement('div');
      syncHeader.className = 'track-menu-section-title';
      syncHeader.textContent = '⏱️ Subtitle Sync';
      subMenu.appendChild(syncHeader);

      const syncRow = document.createElement('div');
      syncRow.className = 'sub-sync-control-row';
      syncRow.onclick = (e) => e.stopPropagation();

      const btnEarlier = document.createElement('button');
      btnEarlier.type = 'button';
      btnEarlier.className = 'btn-sync-adj';
      btnEarlier.title = 'Show subtitles 0.5s earlier (Key: [)';
      btnEarlier.textContent = '-0.5s';
      btnEarlier.onclick = (e) => {
        e.stopPropagation();
        adjustSubtitleOffset(-500);
      };

      const syncLabel = document.createElement('span');
      syncLabel.className = 'sub-sync-label';
      const offsetSec = (currentSubOffsetMs / 1000).toFixed(1);
      syncLabel.textContent = `${offsetSec > 0 ? '+' : ''}${offsetSec}s`;

      const btnLater = document.createElement('button');
      btnLater.type = 'button';
      btnLater.className = 'btn-sync-adj';
      btnLater.title = 'Show subtitles 0.5s later (Key: ])';
      btnLater.textContent = '+0.5s';
      btnLater.onclick = (e) => {
        e.stopPropagation();
        adjustSubtitleOffset(500);
      };

      const btnReset = document.createElement('button');
      btnReset.type = 'button';
      btnReset.className = 'btn-sync-adj';
      btnReset.title = 'Reset to 0.0s';
      btnReset.textContent = 'Reset';
      btnReset.onclick = (e) => {
        e.stopPropagation();
        resetSubtitleOffset();
      };

      syncRow.appendChild(btnEarlier);
      syncRow.appendChild(syncLabel);
      syncRow.appendChild(btnLater);
      syncRow.appendChild(btnReset);
      subMenu.appendChild(syncRow);
    }

    if (subBtn) {
      if (currentSubTrack >= 0) {
        const activeSub = subTracks.find(s => s.index === currentSubTrack);
        const activeDesc = activeSub ? (activeSub.title || (activeSub.language && activeSub.language !== 'und' ? activeSub.language.toUpperCase() : `Sub ${activeSub.index + 1}`)) : `Subs (#${currentSubTrack + 1})`;
        subBtn.textContent = `💬 ${activeDesc}`;
        subBtn.classList.add('has-active');
      } else {
        subBtn.textContent = '💬 Subs';
        subBtn.classList.remove('has-active');
      }
    }
  }
}

function reloadCurrentStream() {
  if (!activeChannel) return;
  stopStream();
  currentStreamingChannelId = null;
  startStream(activeChannel);
}

let isSwitchingTracks = false;
let currentSwitchSessionId = 0;

function switchStreamTracks(channel) {
  if (!channel) return;
  if (!player || currentStreamingChannelId !== channel.id) {
    reloadCurrentStream();
    return;
  }

  const currentVid = video;
  const nextVid = (currentVid && currentVid.id === 'tv-video')
    ? document.getElementById('tv-video-next')
    : document.getElementById('tv-video');

  if (!currentVid || !nextVid) {
    reloadCurrentStream();
    return;
  }

  // Prevent old video from triggering ended event during handoff
  currentVid._ignoreEnded = true;
  nextVid._ignoreEnded = false;

  isSwitchingTracks = true;
  const switchId = ++currentSwitchSessionId;

  const audioBtn = document.getElementById('btn-audio-select');
  const subBtn = document.getElementById('btn-sub-select');
  if (audioBtn) audioBtn.classList.add('track-switching');
  if (subBtn) subBtn.classList.add('track-switching');

  if (currentSubTrack >= 0) {
    showToast('💬 Applying subtitles...', 'info');
  }

  // Cancel any prior in-flight background switch
  if (window._nextPlayer) {
    try {
      window._nextPlayer.pause();
      window._nextPlayer.unload();
      window._nextPlayer.detachMediaElement();
      window._nextPlayer.destroy();
    } catch (_) {}
    window._nextPlayer = null;
  }

  const cleanupSwitchUI = () => {
    isSwitchingTracks = false;
    if (audioBtn) audioBtn.classList.remove('track-switching');
    if (subBtn) subBtn.classList.remove('track-switching');
  };

  // Match current aspect ratio class
  const currentArClass = Array.from(currentVid.classList).find(c => c.startsWith('ar-')) || 'ar-auto';
  nextVid.className = `tv-video-stage ${currentArClass} video-stage-next`;
  nextVid.style.opacity = '0';
  nextVid.muted = true;
  nextVid.volume = currentVid.volume;
  nextVid.playbackRate = currentVid.playbackRate || 1.0;

  const streamUrl = `/api/stream?channel_id=${channel.id}&session_id=${currentSessionID}&audio_track=${currentAudioTrack}&sub_track=${currentSubTrack}&sub_offset=${currentSubOffsetMs}&_sw=${Date.now()}`;

  let handoffDone = false;
  let checkReadyHandler = null;
  let fallbackTimer = null;
  let timeoutTimer = null;

  const cleanupCandidate = () => {
    if (checkReadyHandler) {
      nextVid.removeEventListener('timeupdate', checkReadyHandler);
      nextVid.removeEventListener('playing', checkReadyHandler);
      checkReadyHandler = null;
    }
    clearTimeout(fallbackTimer);
    clearTimeout(timeoutTimer);
    if (!handoffDone && currentVid) {
      currentVid._ignoreEnded = false;
    }
  };

  const performHandoff = () => {
    if (handoffDone || switchId !== currentSwitchSessionId) return;
    handoffDone = true;
    cleanupCandidate();

    currentVid._ignoreEnded = true;
    nextVid._ignoreEnded = false;

    // 1. Unmute nextVid and inherit sound & play state
    nextVid.muted = currentVid.muted;
    nextVid.volume = currentVid.volume;
    nextVid.playbackRate = currentVid.playbackRate || 1.0;
    if (currentVid.paused) {
      nextVid.pause();
    } else {
      nextVid.play().catch(e => console.log('[Player] Next player resume error:', e));
    }

    // 2. Immediately mute old video so there is no audio echo during visual crossfade
    currentVid.muted = true;

    // 3. Smooth visual crossfade via CSS transitions
    nextVid.classList.remove('video-stage-next');
    nextVid.classList.add('active-stage');
    nextVid.style.opacity = '1';

    currentVid.classList.remove('active-stage');
    currentVid.classList.add('video-stage-next');
    currentVid.style.opacity = '0';

    // 4. Update global player and video references
    const oldPlayer = player;
    const oldVid = currentVid;
    video = nextVid;
    player = window._nextPlayer;
    window._nextPlayer = null;

    // 5. Update UI
    cleanupSwitchUI();
    updateTimeshiftUI();
    updateMobileVideoHeight();

    // 6. Disconnect old player's network stream after visual crossfade (250ms) to free server resources,
    // but KEEP its in-browser MSE buffer intact so the user can rewind and see what played before!
    setTimeout(() => {
      if (oldPlayer) {
        try {
          oldPlayer.pause();
          oldPlayer.unload(); // Closes network connection to server without destroying MediaSource buffer
        } catch (e) {
          console.warn('[Player] Error unloading old player network:', e);
        }
      }
    }, 250);

    // Snapshot buffer range of the retiring video to bridge timelapse
    const oldBuf = (oldVid && oldVid.buffered && oldVid.buffered.length > 0)
      ? { start: oldVid.buffered.start(0), end: oldVid.buffered.end(oldVid.buffered.length - 1) }
      : { start: 0, end: oldVid.currentTime || 0 };

    cleanHandoffBridge();

    handoffBridge = {
      prevVideo: oldVid,
      prevPlayer: oldPlayer,
      prevStart: oldBuf.start,
      prevEnd: oldBuf.end,
      prevDuration: Math.max(0, oldBuf.end - oldBuf.start),
      nextStart: nextVid.currentTime || 0,
      nextVideo: nextVid,
      nextPlayer: player,
    };
  };

  const checkReady = () => {
    if (nextVid.currentTime > 0) {
      performHandoff();
    }
  };
  checkReadyHandler = checkReady;
  nextVid.addEventListener('timeupdate', checkReady);
  nextVid.addEventListener('playing', () => setTimeout(checkReady, 50));

  if (window.mpegts && mpegts.getFeatureList().mseLivePlayback) {
    const nextPlayer = mpegts.createPlayer({
      type: 'mpegts',
      isLive: true,
      url: window.location.origin + streamUrl,
    }, {
      enableWorker: false,
      lazyLoad: false,
      stashInitialSize: 64 * 1024,
      autoCleanupSourceBuffer: true,
      autoCleanupMaxBackwardDuration: 120,
      autoCleanupMinBackwardDuration: 60,
      liveBufferLatencyChasing: false,
      liveSync: false,
    });

    window._nextPlayer = nextPlayer;
    nextPlayer.attachMediaElement(nextVid);
    nextPlayer.load();

    nextPlayer.on(mpegts.Events.ERROR, (errorType, errorDetail, errorInfo) => {
      if (switchId !== currentSwitchSessionId || handoffDone) return;
      console.warn('[Player] Candidate track switch error:', errorType, errorDetail, errorInfo);
      cleanupCandidate();
      cleanupSwitchUI();

      try {
        nextPlayer.pause();
        nextPlayer.unload();
        nextPlayer.detachMediaElement();
        nextPlayer.destroy();
      } catch (_) {}
      if (window._nextPlayer === nextPlayer) window._nextPlayer = null;

      if (currentSubTrack >= 0) {
        showToast('⚠️ Subtitles not available in this broadcast; keeping current stream', 'warning');
        currentSubTrack = -1;
        renderTrackMenus();
      }
    });

    nextPlayer.on(mpegts.Events.MEDIA_INFO, () => {
      if (switchId !== currentSwitchSessionId || handoffDone) return;
      nextVid.play().catch(e => console.log('[Player] Candidate play error:', e));
    });

    nextPlayer.play().catch(e => {
      console.log('[Player] Candidate play error:', e);
    });

    fallbackTimer = setTimeout(() => {
      if (!handoffDone && switchId === currentSwitchSessionId && nextVid.readyState >= 2) {
        performHandoff();
      }
    }, 3500);

    timeoutTimer = setTimeout(() => {
      if (switchId !== currentSwitchSessionId || handoffDone) return;
      cleanupCandidate();
      cleanupSwitchUI();

      try {
        nextPlayer.pause();
        nextPlayer.unload();
        nextPlayer.detachMediaElement();
        nextPlayer.destroy();
      } catch (_) {}
      if (window._nextPlayer === nextPlayer) window._nextPlayer = null;

      if (currentSubTrack >= 0) {
        showToast('⚠️ Subtitle stream timed out; keeping current stream', 'warning');
        currentSubTrack = -1;
        renderTrackMenus();
      }
    }, 9000);
  } else {
    nextVid.src = streamUrl;
    nextVid.play().catch(e => console.log('[Player] Fallback play error:', e));
    fallbackTimer = setTimeout(() => {
      if (!handoffDone && switchId === currentSwitchSessionId && nextVid.readyState >= 2) {
        performHandoff();
      }
    }, 3500);
    timeoutTimer = setTimeout(() => {
      if (switchId !== currentSwitchSessionId || handoffDone) return;
      cleanupCandidate();
      cleanupSwitchUI();
    }, 9000);
  }
}

// Track button click handlers
const btnAudioSelect = document.getElementById('btn-audio-select');
const btnSubSelect = document.getElementById('btn-sub-select');
const audioTracksMenu = document.getElementById('audio-tracks-menu');
const subTracksMenu = document.getElementById('sub-tracks-menu');

if (btnAudioSelect && audioTracksMenu) {
  btnAudioSelect.onclick = (e) => {
    e.stopPropagation();
    const arMenu = document.getElementById('aspect-ratio-menu');
    if (arMenu) arMenu.classList.add('hidden');
    if (subTracksMenu) subTracksMenu.classList.add('hidden');
    audioTracksMenu.classList.toggle('hidden');
  };
}

if (btnSubSelect && subTracksMenu) {
  btnSubSelect.onclick = (e) => {
    e.stopPropagation();
    const arMenu = document.getElementById('aspect-ratio-menu');
    if (arMenu) arMenu.classList.add('hidden');
    if (audioTracksMenu) audioTracksMenu.classList.add('hidden');
    subTracksMenu.classList.toggle('hidden');
  };
}

document.addEventListener('click', () => {
  if (audioTracksMenu) audioTracksMenu.classList.add('hidden');
  if (subTracksMenu) subTracksMenu.classList.add('hidden');
  const arMenu = document.getElementById('aspect-ratio-menu');
  if (arMenu) arMenu.classList.add('hidden');
});

// ==========================================================
// INSTANT RECORDING
// ==========================================================
function updateInstantRecordButton() {
  const btn = document.getElementById('btn-record-instant');
  if (!btn) return;
  if (!activeChannel) {
    btn.classList.add('hidden');
    return;
  }
  btn.classList.remove('hidden');

  const isRecordingThisChannel = currentTuners && currentTuners.some(t => 
    t.status === 'active' && 
    t.active_recording && 
    t.active_recording.channel_id === activeChannel.id
  );

  const text = btn.querySelector('.rec-text');

  if (isRecordingThisChannel) {
    btn.classList.add('rec-active');
    btn.classList.add('is-recording');
    btn.title = `Stop Recording "${activeChannel.name}"`;
    if (text) text.textContent = 'Recording';
  } else {
    btn.classList.remove('rec-active');
    btn.classList.remove('is-recording');
    btn.title = `Instant Record "${activeChannel.name}"`;
    if (text) text.textContent = 'Record';
  }
}

const btnRecordInstant = document.getElementById('btn-record-instant');
if (btnRecordInstant) {
  btnRecordInstant.onclick = async () => {
    if (!activeChannel) return;

    // Check if actively recording this channel
    const activeTunerWithRec = currentTuners && currentTuners.find(t => 
      t.status === 'active' && 
      t.active_recording && 
      t.active_recording.channel_id === activeChannel.id
    );

    if (activeTunerWithRec && activeTunerWithRec.active_recording) {
      const rec = activeTunerWithRec.active_recording;
      const confirmed = await showConfirm(`A recording of "${rec.title}" is currently active on ${activeChannel.name}. Do you want to stop recording now?`, 'Active Recording', 'Stop Recording', true);
      if (confirmed) {
        try {
          await fetch(`/api/recordings/${rec.recording_id}/stop`, { method: 'POST' });
          showToast(`⏺ Stopped recording on ${activeChannel.name}`, 'success');
          loadRecordings();
          updateInstantRecordButton();
  updateTimeshiftUI();
          updateHeaderTunerBadge();
          renderChannelList();
        } catch (e) {
          console.error('Failed to stop recording:', e);
        }
      }
      return;
    }

    // Start instant recording
    try {
      const res = await fetch('/api/recordings/instant', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          channel_id: activeChannel.id,
          duration: 3600 // 1 hour default
        })
      });

      if (res.status === 409) {
        const conflict = await res.json();
        showConflictModal({
          message: conflict.error || 'All tuners are busy',
          has_active_recording: conflict.has_active_recording,
          active_recordings: conflict.active_recordings,
          active_tuners: conflict.active_tuners,
          target_channel: activeChannel
        });
        return;
      }

      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        showToast(`⚠️ Could not start recording: ${err.error || res.statusText}`, 'error');
        return;
      }

      const rec = await res.json();
      showToast(`🔴 Recording started for ${activeChannel.name}: "${rec.title}"`);
      updateInstantRecordButton();
  updateTimeshiftUI();
      renderChannelList();
    } catch (e) {
      console.error('Failed to start instant recording:', e);
    }
  };
}

// ==========================================================
// DVR RECORDINGS & SCHEDULER
// ==========================================================
let currentRecordings = [];
let recordingStorageStats = null;
let recSearchQuery = '';
let recSelectedChannel = 'all';
let recSelectedSort = 'newest';
let isRecFiltersInitialized = false;

async function loadRecordings() {
  try {
    const [resRecs, resStorage] = await Promise.all([
      fetch('/api/recordings'),
      fetch('/api/recordings/storage').catch(() => null)
    ]);

    if (resRecs && resRecs.ok) {
      const data = await resRecs.json();
      currentRecordings = Array.isArray(data) ? data : [];
    } else {
      currentRecordings = [];
    }
    if (resStorage && resStorage.ok) {
      recordingStorageStats = await resStorage.json();
    }
    initRecordingsFilterControls();
    renderRecordingsStats();
    renderRecordingsList();
  } catch (e) {
    console.error('Failed to load recordings:', e);
  }
}

function getRecordingChannelName(r) {
  if (r.channel_name && r.channel_name !== 'Unknown') return r.channel_name;
  if (channels && channels.length > 0) {
    const found = channels.find(c => c.id === r.channel_id);
    if (found && found.name) return found.name;
  }
  if (r.file_path) {
    const base = r.file_path.split('/').pop().split('\\').pop();
    const parts = base.split('_');
    if (parts.length >= 3) {
      return parts[1].replace(/-/g, ' ');
    }
  }
  return `Channel ${r.channel_id}`;
}

function renderRecordingsStats() {
  const statCount = document.getElementById('stat-total-count');
  const statSize = document.getElementById('stat-total-size');
  const statTime = document.getElementById('stat-total-time');
  const statDisk = document.getElementById('stat-disk-free');

  const list = Array.isArray(currentRecordings) ? currentRecordings : [];
  const totalCount = list.length;
  let totalBytes = 0;
  let totalDuration = 0;

  list.forEach(r => {
    if (r.file_size) totalBytes += r.file_size;
    if (r.duration) totalDuration += r.duration;
  });

  if (statCount) statCount.textContent = `${totalCount}`;
  if (statSize) statSize.textContent = formatBytes(recordingStorageStats?.total_bytes ?? totalBytes);
  if (statTime) statTime.textContent = formatDuration(recordingStorageStats?.total_duration ?? totalDuration);
  if (statDisk) {
    if (recordingStorageStats && recordingStorageStats.free_bytes) {
      statDisk.textContent = `${formatBytes(recordingStorageStats.free_bytes)} Free`;
      statDisk.title = `Total disk capacity: ${formatBytes(recordingStorageStats.total_disk_bytes)}`;
    } else {
      statDisk.textContent = 'Available';
    }
  }
}

function initRecordingsFilterControls() {
  if (isRecFiltersInitialized) {
    updateRecordingsChannelFilterOptions();
    return;
  }
  isRecFiltersInitialized = true;

  const searchInput = document.getElementById('rec-search-input');
  const clearSearchBtn = document.getElementById('btn-clear-rec-search');
  const channelFilter = document.getElementById('rec-channel-filter');
  const sortSelect = document.getElementById('rec-sort-select');
  const clearFailedBtn = document.getElementById('btn-clear-failed');

  if (searchInput) {
    searchInput.addEventListener('input', (e) => {
      recSearchQuery = e.target.value.trim().toLowerCase();
      if (clearSearchBtn) {
        clearSearchBtn.classList.toggle('hidden', recSearchQuery.length === 0);
      }
      renderRecordingsList();
    });
  }

  if (clearSearchBtn) {
    clearSearchBtn.onclick = () => {
      recSearchQuery = '';
      if (searchInput) searchInput.value = '';
      clearSearchBtn.classList.add('hidden');
      renderRecordingsList();
    };
  }

  if (channelFilter) {
    channelFilter.addEventListener('change', (e) => {
      recSelectedChannel = e.target.value;
      renderRecordingsList();
    });
  }

  if (sortSelect) {
    sortSelect.addEventListener('change', (e) => {
      recSelectedSort = e.target.value;
      renderRecordingsList();
    });
  }

  if (clearFailedBtn) {
    clearFailedBtn.onclick = async () => {
      const confirmed = await showConfirm(
        'Clear all failed recordings from the database?',
        'Clear Failed Recordings',
        'Clear Failed',
        true
      );
      if (confirmed) {
        try {
          const res = await fetch('/api/recordings/clear-failed', { method: 'POST' });
          if (res.ok) {
            const data = await res.json();
            showToast(`Cleared ${data.cleared || 0} failed recording(s)`, 'success');
            loadRecordings();
          }
        } catch (err) {
          showToast('Failed to clear recordings', 'danger');
        }
      }
    };
  }

  updateRecordingsChannelFilterOptions();
}

function updateRecordingsChannelFilterOptions() {
  const channelFilter = document.getElementById('rec-channel-filter');
  if (!channelFilter) return;

  const list = Array.isArray(currentRecordings) ? currentRecordings : [];
  const channelNames = new Set();
  list.forEach(r => {
    const ch = getRecordingChannelName(r);
    if (ch) channelNames.add(ch);
  });

  const currentVal = channelFilter.value || 'all';
  channelFilter.innerHTML = '<option value="all">All Channels</option>';
  Array.from(channelNames).sort().forEach(name => {
    const opt = document.createElement('option');
    opt.value = name;
    opt.textContent = `📺 ${name}`;
    if (name === currentVal) opt.selected = true;
    channelFilter.appendChild(opt);
  });
}

function formatBytes(bytes) {
  if (!bytes || bytes <= 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function formatDuration(sec) {
  if (!sec || sec <= 0) return '0m';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function renderRecordingsList() {
  const activeList = document.getElementById('active-recordings-list');
  const schedList = document.getElementById('scheduled-recordings-list');
  const compList = document.getElementById('completed-recordings-list');
  const activeCount = document.getElementById('active-rec-count');
  const schedCount = document.getElementById('sched-rec-count');
  const compCount = document.getElementById('completed-rec-count');
  const filterCountEl = document.getElementById('rec-filter-count');
  const clearFailedBtn = document.getElementById('btn-clear-failed');

  const list = Array.isArray(currentRecordings) ? currentRecordings : [];
  const active = list.filter(r => r.status === 'recording');
  const scheduled = list.filter(r => r.status === 'scheduled');
  const completed = list.filter(r => r.status === 'completed' || r.status === 'failed' || r.status === 'cancelled');

  if (activeCount) activeCount.textContent = active.length;
  if (schedCount) schedCount.textContent = scheduled.length;
  if (compCount) compCount.textContent = completed.length;

  const failedCount = completed.filter(r => r.status === 'failed').length;
  if (clearFailedBtn) {
    if (failedCount > 0) {
      clearFailedBtn.classList.remove('hidden');
      clearFailedBtn.textContent = `🗑️ Clear Failed (${failedCount})`;
    } else {
      clearFailedBtn.classList.add('hidden');
    }
  }

  // 1. Active Recordings
  if (activeList) {
    activeList.innerHTML = '';
    if (active.length === 0) {
      activeList.className = 'recordings-grid empty-grid';
      activeList.innerHTML = '<div class="empty-state">No broadcasts are actively being recorded.</div>';
    } else {
      activeList.className = 'recordings-grid';
      active.forEach(r => {
        const chName = getRecordingChannelName(r);
        const start = new Date(r.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        const elapsedSec = Math.max(0, Math.floor((Date.now() - new Date(r.start_time).getTime()) / 1000));
        const plannedDuration = r.duration || 3600;
        const progressPct = Math.min(100, Math.max(5, Math.round((elapsedSec / plannedDuration) * 100)));

        const card = document.createElement('div');
        card.className = 'recording-card card-active';
        card.innerHTML = `
          <div class="rec-card-header">
            <div class="rec-card-title-wrap">
              <div class="rec-card-title">${r.title}</div>
              <span class="rec-card-channel-badge">📺 ${chName}</span>
            </div>
            <span class="rec-status-tag active"><span class="live-dot pulse" style="width:7px;height:7px;margin-right:3px;"></span> Recording</span>
          </div>
          <div class="rec-card-body">
            <div class="rec-meta-grid">
              <span class="rec-meta-chip">⏱️ Started: ${start} (${formatDuration(elapsedSec)} elapsed)</span>
              <span class="rec-meta-chip">💾 Captured: ${formatBytes(r.file_size)}</span>
            </div>
            <div class="rec-progress-wrap" title="Recording progress: ${progressPct}%">
              <div class="rec-progress-bar" style="width: ${progressPct}%;"></div>
            </div>
          </div>
          <div class="rec-card-actions">
            <button class="btn-rec-action stop-btn" data-id="${r.id}">⏹ Stop Recording</button>
          </div>
        `;
        card.querySelector('.stop-btn').onclick = async (e) => {
          e.stopPropagation();
          const confirmed = await showConfirm(`Stop recording "${r.title}"?`, 'Stop Recording', 'Stop Recording', true);
          if (confirmed) {
            await fetch(`/api/recordings/${r.id}/stop`, { method: 'POST' });
            showToast('⏺ Recording stopped', 'success');
            loadRecordings();
            updateInstantRecordButton();
  updateTimeshiftUI();
            updateHeaderTunerBadge();
          }
        };
        activeList.appendChild(card);
      });
    }
  }

  // 2. Scheduled Recordings
  if (schedList) {
    schedList.innerHTML = '';
    if (scheduled.length === 0) {
      schedList.className = 'recordings-grid empty-grid';
      schedList.innerHTML = '<div class="empty-state">No upcoming recordings scheduled. Click "Schedule Recording" or tap any program in the TV Guide.</div>';
    } else {
      schedList.className = 'recordings-grid';
      scheduled.forEach(r => {
        const chName = getRecordingChannelName(r);
        const startDate = new Date(r.start_time).toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
        const startTime = new Date(r.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        const endTime = new Date(r.end_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

        const card = document.createElement('div');
        card.className = 'recording-card';
        card.innerHTML = `
          <div class="rec-card-header">
            <div class="rec-card-title-wrap">
              <div class="rec-card-title">${r.title}</div>
              <span class="rec-card-channel-badge">📺 ${chName}</span>
            </div>
            <span class="rec-status-tag scheduled">⏱️ Scheduled</span>
          </div>
          <div class="rec-card-body">
            ${r.description ? `<div class="rec-card-desc" title="${r.description}">${r.description}</div>` : ''}
            <div class="rec-meta-grid">
              <span class="rec-meta-chip">📅 ${startDate} • ${startTime} - ${endTime}</span>
              <span class="rec-meta-chip">⏳ Duration: ${formatDuration(r.duration)}</span>
            </div>
          </div>
          <div class="rec-card-actions">
            <button class="btn-rec-action del-btn" data-id="${r.id}">❌ Cancel Timer</button>
          </div>
        `;
        card.querySelector('.del-btn').onclick = async (e) => {
          e.stopPropagation();
          const confirmed = await showConfirm(`Cancel scheduled recording for "${r.title}"?`, 'Cancel Timer', 'Cancel Timer', true);
          if (confirmed) {
            await fetch(`/api/recordings/${r.id}`, { method: 'DELETE' });
            showToast('⏱️ Scheduled recording cancelled', 'info');
            loadRecordings();
          }
        };
        schedList.appendChild(card);
      });
    }
  }

  // 3. Completed Recordings (Filtered & Sorted)
  if (compList) {
    compList.innerHTML = '';

    // Apply Filter & Search
    let filtered = completed.filter(r => {
      const chName = getRecordingChannelName(r);
      if (recSelectedChannel !== 'all' && chName !== recSelectedChannel) {
        return false;
      }
      if (recSearchQuery) {
        const titleMatch = (r.title || '').toLowerCase().includes(recSearchQuery);
        const descMatch = (r.description || '').toLowerCase().includes(recSearchQuery);
        const chMatch = chName.toLowerCase().includes(recSearchQuery);
        if (!titleMatch && !descMatch && !chMatch) return false;
      }
      return true;
    });

    // Apply Sort
    filtered.sort((a, b) => {
      if (recSelectedSort === 'oldest') {
        return new Date(a.start_time) - new Date(b.start_time);
      } else if (recSelectedSort === 'title') {
        return (a.title || '').localeCompare(b.title || '');
      } else if (recSelectedSort === 'duration') {
        return (b.duration || 0) - (a.duration || 0);
      } else if (recSelectedSort === 'size') {
        return (b.file_size || 0) - (a.file_size || 0);
      } else {
        // 'newest' default
        return new Date(b.start_time) - new Date(a.start_time);
      }
    });

    if (filterCountEl) {
      if (recSearchQuery || recSelectedChannel !== 'all') {
        filterCountEl.textContent = `Showing ${filtered.length} of ${completed.length} recordings`;
      } else {
        filterCountEl.textContent = `${completed.length} total recordings`;
      }
    }

    if (filtered.length === 0) {
      compList.className = 'recordings-grid empty-grid';
      if (completed.length === 0) {
        compList.innerHTML = '<div class="empty-state">No saved recordings found. You can schedule recordings from the TV Guide or start an instant recording anytime.</div>';
      } else {
        compList.innerHTML = '<div class="empty-state">No recordings matched your search/filter criteria.</div>';
      }
    } else {
      compList.className = 'recordings-grid';
      filtered.forEach(r => {
        const chName = getRecordingChannelName(r);
        const dateStr = new Date(r.start_time).toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
        const timeStr = new Date(r.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        const isFailed = r.status === 'failed';
        const tagClass = isFailed ? 'failed' : 'completed';
        const tagText = isFailed ? 'Failed' : 'Completed';

        const card = document.createElement('div');
        card.className = 'recording-card';
        card.style.cursor = !isFailed ? 'pointer' : 'default';
        card.innerHTML = `
          <div class="rec-card-header">
            <div class="rec-card-title-wrap">
              <div class="rec-card-title">${r.title}</div>
              <span class="rec-card-channel-badge">📺 ${chName}</span>
            </div>
            <span class="rec-status-tag ${tagClass}">${tagText}</span>
          </div>
          <div class="rec-card-body">
            ${r.description ? `<div class="rec-card-desc" title="${r.description}">${r.description}</div>` : ''}
            <div class="rec-meta-grid">
              <span class="rec-meta-chip">📅 Recorded: ${dateStr} • ${timeStr}</span>
              <span class="rec-meta-chip">⏳ ${formatDuration(r.duration)}</span>
              <span class="rec-meta-chip">💾 ${formatBytes(r.file_size)}</span>
            </div>
            ${r.error_message ? `<div class="rec-info-row" style="color: var(--danger); font-weight: 500;">⚠️ ${r.error_message}</div>` : ''}
          </div>
          <div class="rec-card-actions">
            ${!isFailed ? `<button class="btn-rec-action play-btn" data-id="${r.id}">▶ Play</button>` : ''}
            ${!isFailed ? `<a href="/api/recordings/${r.id}/download" download class="btn-rec-action download-btn">⬇ Download</a>` : ''}
            <button class="btn-rec-action del-btn" data-id="${r.id}">🗑 Delete</button>
          </div>
        `;

        if (!isFailed) {
          card.onclick = (e) => {
            if (e.target.closest('button') || e.target.closest('a')) return;
            openRecordingPlayer(r);
          };
          const playBtn = card.querySelector('.play-btn');
          if (playBtn) {
            playBtn.onclick = (e) => {
              e.stopPropagation();
              openRecordingPlayer(r);
            };
          }
          const dlBtn = card.querySelector('.download-btn');
          if (dlBtn) {
            dlBtn.onclick = (e) => e.stopPropagation();
          }
        }

        card.querySelector('.del-btn').onclick = async (e) => {
          e.stopPropagation();
          const confirmed = await showConfirm(`Delete recording "${r.title}"? This will delete the file from disk.`, 'Delete Recording', 'Delete Permanently', true);
          if (confirmed) {
            await fetch(`/api/recordings/${r.id}`, { method: 'DELETE' });
            showToast('🗑 Recording deleted', 'info');
            loadRecordings();
          }
        };

        compList.appendChild(card);
      });
    }
  }
}

// Completed Recording Player Modal
let recMpegtsPlayer = null;

function openRecordingPlayer(rec) {
  const modal = document.getElementById('recording-player-modal');
  const title = document.getElementById('rec-player-title');
  const videoEl = document.getElementById('rec-video-player');
  if (!modal || !videoEl) return;

  if (title) title.textContent = `${rec.title} (${rec.channel_name})`;

  closeRecordingPlayer();
  modal.classList.remove('hidden');

  const streamUrl = `/api/recordings/${rec.id}/stream`;

  videoEl.onerror = () => {
    console.warn('[RecPlayer] Native HTML5 play failed, falling back to mpegts.js');
    if (window.mpegts && mpegts.getFeatureList().msePlayback) {
      recMpegtsPlayer = mpegts.createPlayer({
        type: 'mpegts',
        isLive: false,
        url: window.location.origin + streamUrl,
      }, {
        enableWorker: false,
        lazyLoad: false,
      });
      recMpegtsPlayer.attachMediaElement(videoEl);
      recMpegtsPlayer.load();
      recMpegtsPlayer.play().catch(e => console.warn('[RecPlayer] mpegts play error:', e));
    }
  };

  videoEl.src = streamUrl;
  videoEl.load();
  videoEl.play().catch(e => console.log('[RecPlayer] Play notice:', e));
}

function closeRecordingPlayer() {
  const modal = document.getElementById('recording-player-modal');
  const videoEl = document.getElementById('rec-video-player');

  if (recMpegtsPlayer) {
    try {
      recMpegtsPlayer.pause();
      recMpegtsPlayer.unload();
      recMpegtsPlayer.detachMediaElement();
      recMpegtsPlayer.destroy();
    } catch (e) {
      console.warn('[RecPlayer] Destroy error:', e);
    }
    recMpegtsPlayer = null;
  }

  if (videoEl) {
    videoEl.pause();
    videoEl.onerror = null;
    videoEl.removeAttribute('src');
    videoEl.load();
  }
  if (modal) modal.classList.add('hidden');
}

const btnCloseRecPlayer = document.getElementById('btn-close-rec-player');
if (btnCloseRecPlayer) btnCloseRecPlayer.onclick = closeRecordingPlayer;
const recPlayerModal = document.getElementById('recording-player-modal');
if (recPlayerModal) {
  recPlayerModal.onclick = (e) => {
    if (e.target.id === 'recording-player-modal') closeRecordingPlayer();
  };
}

// Manual Schedule Modal
const btnOpenSchedModal = document.getElementById('btn-open-schedule-modal');
const manualSchedModal = document.getElementById('manual-schedule-modal');
const btnCloseSchedModal = document.getElementById('btn-close-schedule-modal');
const btnCancelManualSched = document.getElementById('btn-cancel-manual-sched');
const scheduleForm = document.getElementById('schedule-recording-form');

if (btnOpenSchedModal) {
  btnOpenSchedModal.onclick = () => {
    const select = document.getElementById('sched-channel-select');
    if (select) {
      select.innerHTML = '';
      channels.forEach(c => {
        const opt = document.createElement('option');
        opt.value = c.id;
        opt.textContent = `${c.number}. ${c.name}`;
        select.appendChild(opt);
      });
      if (activeChannel) select.value = activeChannel.id;
    }

    const startInput = document.getElementById('sched-start-input');
    if (startInput) {
      // Default to 5 minutes from now in local ISO format (YYYY-MM-DDTHH:MM)
      const now = new Date(Date.now() + 5 * 60 * 1000);
      const localIso = new Date(now.getTime() - now.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
      startInput.value = localIso;
    }

    if (manualSchedModal) manualSchedModal.classList.remove('hidden');
  };
}

function closeManualScheduleModal() {
  if (manualSchedModal) manualSchedModal.classList.add('hidden');
}

if (btnCloseSchedModal) btnCloseSchedModal.onclick = closeManualScheduleModal;
if (btnCancelManualSched) btnCancelManualSched.onclick = closeManualScheduleModal;
if (manualSchedModal) {
  manualSchedModal.onclick = (e) => {
    if (e.target.id === 'manual-schedule-modal') closeManualScheduleModal();
  };
}

if (scheduleForm) {
  scheduleForm.onsubmit = async (e) => {
    e.preventDefault();
    const chId = parseInt(document.getElementById('sched-channel-select').value, 10);
    const title = document.getElementById('sched-title-input').value.trim();
    const startVal = document.getElementById('sched-start-input').value;
    const durMin = parseInt(document.getElementById('sched-duration-input').value, 10);

    const startTime = new Date(startVal);
    const endTime = new Date(startTime.getTime() + durMin * 60 * 1000);

    const prePad = parseInt(document.getElementById('sched-pre-padding').value, 10);
    const postPad = parseInt(document.getElementById('sched-post-padding').value, 10);

    try {
      const res = await fetch('/api/recordings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          channel_id: chId,
          title: title,
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          duration: durMin * 60,
          pre_padding: prePad,
          post_padding: postPad
        })
      });

      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        showToast(`⚠️ Failed to schedule recording: ${err.error || res.statusText}`, 'error');
        return;
      }

      closeManualScheduleModal();
      showToast(`⏱️ Scheduled recording "${title}"`);
      loadRecordings();
    } catch (err) {
      console.error('Error saving scheduled recording:', err);
    }
  };
}

// EPG Modal Schedule Recording
let currentEPGModalEvent = null;
const origShowEPGModal = window.showEPGModal;
window.showEPGModal = function(event, channelName) {
  currentEPGModalEvent = { event, channelName };
  origShowEPGModal(event, channelName);

  const btnEPGRecord = document.getElementById('btn-epg-record');
  if (btnEPGRecord && event) {
    const isRecording = currentTuners && currentTuners.some(t => 
      t.status === 'active' && 
      t.active_recording && 
      t.active_recording.channel_id === event.channel_id
    );

    const now = Date.now();
    const start = new Date(event.start_time).getTime();
    const end = new Date(event.end_time).getTime();
    const isLive = now >= start && now < end;

    if (isRecording) {
      btnEPGRecord.textContent = '⏹ Stop Active Recording';
      btnEPGRecord.className = 'btn btn-danger';
    } else if (isLive) {
      btnEPGRecord.textContent = '⏺ Record Live Program';
      btnEPGRecord.className = 'btn btn-primary';
    } else {
      btnEPGRecord.textContent = '⏱️ Schedule Recording';
      btnEPGRecord.className = 'btn btn-primary';
    }
  }
};

const btnEPGRecord = document.getElementById('btn-epg-record');
if (btnEPGRecord) {
  btnEPGRecord.onclick = async () => {
    if (!currentEPGModalEvent || !currentEPGModalEvent.event) return;
    const ev = currentEPGModalEvent.event;

    // Check if actively recording this channel
    const activeRecTuner = currentTuners && currentTuners.find(t => 
      t.status === 'active' && 
      t.active_recording && 
      t.active_recording.channel_id === ev.channel_id
    );
    if (activeRecTuner && activeRecTuner.active_recording) {
      const rec = activeRecTuner.active_recording;
      const confirmed = await showConfirm(`Stop recording "${rec.title}" on ${currentEPGModalEvent.channelName}?`, 'Stop Recording', 'Stop Recording', true);
      if (confirmed) {
        await fetch(`/api/recordings/${rec.recording_id}/stop`, { method: 'POST' });
        showToast(`⏺ Stopped recording "${rec.title}"`, 'success');
        closeEPGModal();
        loadRecordings();
        updateInstantRecordButton();
  updateTimeshiftUI();
        renderChannelList();
      }
      return;
    }

    const title = getLocalized(ev.titles, currentLanguage) || 'Program';
    const desc = getLocalized(ev.descriptions, currentLanguage) || '';
    const start = new Date(ev.start_time);
    const end = new Date(ev.end_time);
    const durSec = Math.max(60, Math.floor((end - start) / 1000));

    try {
      const res = await fetch('/api/recordings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          channel_id: ev.channel_id,
          title: title,
          description: desc,
          start_time: start.toISOString(),
          end_time: end.toISOString(),
          duration: durSec,
          event_id: ev.event_id
        })
      });

      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        showToast(`⚠️ Failed to schedule recording: ${err.error || res.statusText}`, 'error');
        return;
      }

      closeEPGModal();
      const now = Date.now();
      const isLive = now >= start.getTime() && now < end.getTime();
      if (isLive) {
        showToast(`🔴 Recording started for ${currentEPGModalEvent.channelName}: "${title}"`);
      } else {
        showToast(`⏱️ Scheduled recording "${title}" on ${currentEPGModalEvent.channelName}`);
      }
      loadRecordings();
      updateInstantRecordButton();
  updateTimeshiftUI();
      renderChannelList();
    } catch (e) {
      console.error('Error scheduling EPG recording:', e);
    }
  };
}
