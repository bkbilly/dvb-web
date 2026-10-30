/**
 * DVB-Web Client-Side Mock Provider for GitHub Pages
 * Enables a fully interactive live demo of DVB-Web without requiring a physical DVB tuner or backend server.
 */
(function() {
  'use strict';

  const isGitHubPages = window.location.hostname.endsWith('github.io') || window.location.protocol === 'file:';
  const isMockRequested = new URLSearchParams(window.location.search).has('mock') || window.location.hash.includes('mock');

  if (!isGitHubPages && !isMockRequested) {
    // Real server mode: do not intercept anything!
    return;
  }

  console.info('[DVB-Mock] Activating static GitHub Pages interactive mock demo mode.');

  // ==========================================================
  // 1. MOCK DATA DEFINITIONS
  // ==========================================================
  const MOCK_CHANNELS = [
    {
      id: 1,
      name: 'BBC One HD',
      number: 1,
      frequency: 506000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT2',
      service_id: 101,
      video_pid: 1011,
      audio_pid: 1012,
      enabled: true,
      is_radio: 0,
      has_teletext: 1,
      hbbtv_url: 'https://itv.com',
      logo_url: ''
    },
    {
      id: 2,
      name: 'BBC Two HD',
      number: 2,
      frequency: 506000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT2',
      service_id: 102,
      video_pid: 1021,
      audio_pid: 1022,
      enabled: true,
      is_radio: 0,
      has_teletext: 0,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 3,
      name: 'ITV 1 HD',
      number: 3,
      frequency: 506000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT2',
      service_id: 103,
      video_pid: 1031,
      audio_pid: 1032,
      enabled: true,
      is_radio: 0,
      has_teletext: 1,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 4,
      name: 'Channel 4 HD',
      number: 4,
      frequency: 506000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT2',
      service_id: 104,
      video_pid: 1041,
      audio_pid: 1042,
      enabled: true,
      is_radio: 0,
      has_teletext: 0,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 5,
      name: 'ERT 1 HD',
      number: 5,
      frequency: 522000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT',
      service_id: 1500,
      video_pid: 1501,
      audio_pid: 1502,
      enabled: true,
      is_radio: 0,
      has_teletext: 1,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 6,
      name: 'ANT1 HD',
      number: 6,
      frequency: 522000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT',
      service_id: 1600,
      video_pid: 1601,
      audio_pid: 1602,
      enabled: true,
      is_radio: 0,
      has_teletext: 1,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 7,
      name: 'Mega Channel',
      number: 7,
      frequency: 522000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT',
      service_id: 1700,
      video_pid: 1701,
      audio_pid: 1702,
      enabled: true,
      is_radio: 0,
      has_teletext: 1,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 8,
      name: 'BBC Radio 1',
      number: 10,
      frequency: 506000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT2',
      service_id: 201,
      video_pid: 0,
      audio_pid: 2011,
      enabled: true,
      is_radio: 1,
      has_teletext: 0,
      hbbtv_url: '',
      logo_url: ''
    },
    {
      id: 9,
      name: 'Classic FM',
      number: 11,
      frequency: 506000000,
      bandwidth: 8000000,
      delivery_system: 'DVBT2',
      service_id: 202,
      video_pid: 0,
      audio_pid: 2021,
      enabled: true,
      is_radio: 1,
      has_teletext: 0,
      hbbtv_url: '',
      logo_url: ''
    }
  ];

  const MOCK_SHOW_TEMPLATES = [
    { startH: 6, startM: 0, durH: 3, durM: 0, titleEn: 'Breakfast Live', titleGr: 'Πρωινή Ενημέρωση', descEn: 'Live news, sports, business headlines and weather updates.', descGr: 'Ζωντανή ενημέρωση με ειδήσεις, ρεπορτάζ και καιρό.' },
    { startH: 9, startM: 0, durH: 2, durM: 30, titleEn: 'Morning Magazine Show', titleGr: 'Πρωινό Μαγκαζίνο', descEn: 'Entertainment, celebrity guests, lifestyle and health features.', descGr: 'Ψυχαγωγική εκπομπή με καλεσμένους και θέματα επικαιρότητας.' },
    { startH: 11, startM: 30, durH: 1, durM: 30, titleEn: 'Master Chef: The Kitchen Challenge', titleGr: 'Γεύσεις & Συνταγές', descEn: 'Culinary battle with mouth-watering dishes and culinary tips.', descGr: 'Εκπομπή μαγειρικής με συνταγές και γαστρονομικές προτάσεις.' },
    { startH: 13, startM: 0, durH: 1, durM: 0, titleEn: 'Midday News Hour', titleGr: 'Μεσημβρινό Δελτίο Ειδήσεων', descEn: 'Comprehensive national and international news round-up.', descGr: 'Αναλυτικό δελτίο ειδήσεων με τις σημαντικότερες ειδήσεις.' },
    { startH: 14, startM: 0, durH: 2, durM: 0, titleEn: 'Drama Series: Whispers of the Past', titleGr: 'Δραματική Σειρά: Ψίθυροι', descEn: 'Compelling drama tracking the lives and secrets of two families.', descGr: 'Καθηλωτική δραματική σειρά γεμάτη ανατροπές και μυστικά.' },
    { startH: 16, startM: 0, durH: 1, durM: 30, titleEn: 'Cash or Trash: Antiques Auction', titleGr: 'Δημοπρασία Αντικών', descEn: 'Appraising rare collectibles and negotiating with expert dealers.', descGr: 'Εκτίμηση σπάνιων αντικειμένων και διαπραγματεύσεις με εμπόρους.' },
    { startH: 17, startM: 30, durH: 1, durM: 30, titleEn: 'The Chase: Trivia Challenge', titleGr: 'Το Κυνήγι των Γνώσεων', descEn: 'Contestants pit their general knowledge wits against the Chaser.', descGr: 'Τηλεπαιχνίδι γνώσεων με συναρπαστικές μονομαχίες.' },
    { startH: 19, startM: 0, durH: 1, durM: 0, titleEn: 'Early Evening News & Analysis', titleGr: 'Κεντρικό Δελτίο Ειδήσεων', descEn: 'Live broadcast reports, political debate and economic analysis.', descGr: 'Το κεντρικό δελτίο ειδήσεων με αναλύσεις και ρεπορτάζ.' },
    { startH: 20, startM: 0, durH: 1, durM: 0, titleEn: 'Nature & Wildlife: Planet Earth Explorers', titleGr: 'Ντοκιμαντέρ: Άγρια Φύση', descEn: 'Stunning 4K documentary uncovering remarkable animal migrations.', descGr: 'Εντυπωσιακό ντοκιμαντέρ για τη φύση και την άγρια ζωή.' },
    { startH: 21, startM: 0, durH: 2, durM: 15, titleEn: 'Blockbuster Movie: Beyond The Stars', titleGr: 'Ταινία: Πέρα από τα Άστρα', descEn: 'Sci-Fi adventure following deep-space explorers on an uncharted world.', descGr: 'Επική ταινία επιστημονικής φαντασίας με εντυπωσιακά εφέ.' },
    { startH: 23, startM: 15, durH: 1, durM: 15, titleEn: 'Late Night Talk Show with Greg Norton', titleGr: 'Late Night Show', descEn: 'Comedy, hilarious interviews with Hollywood stars, and live musical performance.', descGr: 'Χιουμοριστική εκπομπή με καλεσμένους και ζωντανή μουσική.' },
    { startH: 0, startM: 30, durH: 2, durM: 30, titleEn: 'Midnight Thriller: Cold Evidence', titleGr: 'Νυχτερινό Θρίλερ', descEn: 'Gripping detective mystery set against a winter city backdrop.', descGr: 'Αστυνομικό θρίλερ μυστηρίου γεμάτο αγωνία.' },
    { startH: 3, startM: 0, durH: 3, durM: 0, titleEn: 'World News Overnight', titleGr: 'Διεθνείς Ειδήσεις', descEn: 'Global headline updates and documentary features through the night.', descGr: 'Συνεχής ενημέρωση με διεθνή θέματα.' }
  ];

  function generateEPGEvents(channelId, fromDate, toDate) {
    const events = [];
    let curDay = new Date(fromDate);
    curDay.setHours(0, 0, 0, 0);

    const endDay = new Date(toDate);
    endDay.setDate(endDay.getDate() + 1);

    const chOffsetMin = ((channelId * 17) % 45); // slight schedule diversity per channel

    while (curDay < endDay) {
      for (let t = 0; t < MOCK_SHOW_TEMPLATES.length; t++) {
        const tpl = MOCK_SHOW_TEMPLATES[t];
        const s = new Date(curDay);
        s.setHours(tpl.startH, tpl.startM + chOffsetMin, 0, 0);
        const e = new Date(s.getTime() + (tpl.durH * 60 + tpl.durM) * 60000);

        if (e > fromDate && s < toDate) {
          events.push({
            id: channelId * 100000 + Math.floor(s.getTime() / 60000),
            channel_id: channelId,
            event_id: Math.floor(s.getTime() / 60000) % 65535,
            start_time: s.toISOString(),
            end_time: e.toISOString(),
            titles: {
              eng: tpl.titleEn,
              ell: tpl.titleGr,
              gre: tpl.titleGr
            },
            descriptions: {
              eng: tpl.descEn,
              ell: tpl.descGr,
              gre: tpl.descGr
            },
            genres: ['Movie/Drama']
          });
        }
      }
      curDay.setDate(curDay.getDate() + 1);
    }
    return events;
  }

  function getChannelNowNext(channelId) {
    const now = new Date();
    const from = new Date(now.getTime() - 4 * 3600000);
    const to = new Date(now.getTime() + 8 * 3600000);
    const evs = generateEPGEvents(channelId, from, to);
    const nowMs = now.getTime();

    let nowShow = null;
    let nextShow = null;

    for (let i = 0; i < evs.length; i++) {
      const s = new Date(evs[i].start_time).getTime();
      const e = new Date(evs[i].end_time).getTime();
      if (nowMs >= s && nowMs < e) {
        nowShow = evs[i];
      } else if (s >= nowMs && (!nextShow || s < new Date(nextShow.start_time).getTime())) {
        nextShow = evs[i];
      }
    }
    return { nowShow, nextShow };
  }

  // Initial recordings stored in localStorage
  function getStoredRecordings() {
    try {
      const raw = localStorage.getItem('dvb_mock_recordings');
      if (raw) return JSON.parse(raw);
    } catch (_) {}
    const now = new Date();
    return [
      {
        id: 1,
        channel_id: 1,
        channel_name: 'BBC One HD',
        event_id: 101,
        title: 'Master Chef: The Kitchen Challenge',
        description: 'Culinary battle with mouth-watering dishes and culinary tips.',
        start_time: new Date(now.getTime() - 3600000).toISOString(),
        end_time: now.toISOString(),
        duration: 3600,
        pre_padding: 2,
        post_padding: 5,
        file_path: '/data/recordings/1_BBC_One_HD_Master_Chef.mp4',
        file_size: 1420580920,
        status: 'completed',
        created_at: new Date(now.getTime() - 3600000).toISOString()
      },
      {
        id: 2,
        channel_id: 3,
        channel_name: 'ITV 1 HD',
        event_id: 102,
        title: 'The Chase: Trivia Challenge',
        description: 'Contestants pit their general knowledge wits against the Chaser.',
        start_time: new Date(now.getTime() + 7200000).toISOString(),
        end_time: new Date(now.getTime() + 10800000).toISOString(),
        duration: 3600,
        pre_padding: 2,
        post_padding: 5,
        file_path: '',
        file_size: 0,
        status: 'scheduled',
        created_at: now.toISOString()
      }
    ];
  }

  function saveStoredRecordings(list) {
    try {
      localStorage.setItem('dvb_mock_recordings', JSON.stringify(list));
    } catch (_) {}
  }

  // Active mock tuners pool (matches backend TunerStatus struct)
  const MOCK_TUNERS = [
    {
      id: 0,
      name: 'Sony CXD2880 DVB-T/T2/C (Simulated)',
      frontend_path: '/dev/dvb/adapter0/frontend0',
      status: 'idle',
      current_frequency: 0,
      current_channel: '',
      active_channels: [],
      enabled: true,
      viewers: [],
      is_mock: true,
      signal: {
        has_lock: true,
        has_signal: true,
        has_carrier: true,
        has_viterbi: true,
        has_sync: true,
        signal_strength: 95,
        signal_quality: 98,
        snr_db: 28.5,
        ber: 0,
        unc: 0
      },
      active_recording: null
    },
    {
      id: 1,
      name: 'Silicon Labs Si2168 DVB-T/T2 (Simulated)',
      frontend_path: '/dev/dvb/adapter1/frontend0',
      status: 'idle',
      current_frequency: 0,
      current_channel: '',
      active_channels: [],
      enabled: true,
      viewers: [],
      is_mock: true,
      signal: {
        has_lock: true,
        has_signal: true,
        has_carrier: true,
        has_viterbi: true,
        has_sync: true,
        signal_strength: 91,
        signal_quality: 95,
        snr_db: 27.0,
        ber: 0,
        unc: 0
      },
      active_recording: null
    }
  ];

  const activeMockSockets = new Set();

  function broadcastTunersUpdate() {
    activeMockSockets.forEach(ws => {
      if (ws.readyState === 1) {
        ws._dispatch({
          event: 'tuners_update',
          tuners: MOCK_TUNERS
        });
      }
    });
  }

  // ==========================================================
  // 2. INTERCEPT FETCH API
  // ==========================================================
  const origFetch = window.fetch;
  window.fetch = async function(url, options) {
    const urlStr = typeof url === 'string' ? url : (url.url || '');
    const reqUrl = new URL(urlStr, window.location.origin);
    const pathname = reqUrl.pathname;

    // A. Channels List
    if (pathname === '/api/channels' && (!options || options.method === 'GET' || !options.method)) {
      const enriched = MOCK_CHANNELS.map(c => {
        const { nowShow, nextShow } = getChannelNowNext(c.id);
        return {
          ...c,
          now_playing: nowShow,
          next_playing: nextShow
        };
      });
      return new Response(JSON.stringify(enriched), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // B. Channel Detail: /api/channels/{id}
    if (pathname.startsWith('/api/channels/')) {
      const idStr = pathname.replace('/api/channels/', '');
      const chId = parseInt(idStr, 10);
      if (!isNaN(chId)) {
        const found = MOCK_CHANNELS.find(c => c.id === chId);
        if (found) {
          const { nowShow, nextShow } = getChannelNowNext(found.id);
          return new Response(JSON.stringify({ ...found, now_playing: nowShow, next_playing: nextShow }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' }
          });
        }
      }
    }

    // C. Channel Tracks: /api/channels/{id}/tracks
    if (pathname.includes('/tracks')) {
      return new Response(JSON.stringify({
        audio_tracks: [
          { index: 0, language: 'eng', codec: 'aac', channels: 2, is_default: true },
          { index: 1, language: 'ell', codec: 'aac', channels: 2, is_default: false }
        ],
        subtitle_tracks: [
          { index: 0, language: 'eng', type: 'dvb_teletext', page: 888 },
          { index: 1, language: 'ell', type: 'dvb_subtitles' }
        ]
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // D. EPG Languages
    if (pathname === '/api/epg/languages') {
      return new Response(JSON.stringify(['eng', 'ell']), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // E. EPG Query: /api/epg
    if (pathname === '/api/epg') {
      const fromParam = reqUrl.searchParams.get('from');
      const toParam = reqUrl.searchParams.get('to');
      const chIdParam = reqUrl.searchParams.get('channel_id');

      const from = fromParam ? new Date(fromParam) : new Date(Date.now() - 2 * 3600000);
      const to = toParam ? new Date(toParam) : new Date(Date.now() + 7 * 86400000);

      let allEvents = [];
      if (chIdParam) {
        allEvents = generateEPGEvents(parseInt(chIdParam, 10), from, to);
      } else {
        MOCK_CHANNELS.forEach(c => {
          allEvents = allEvents.concat(generateEPGEvents(c.id, from, to));
        });
      }
      return new Response(JSON.stringify(allEvents), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // F. EPG Search: /api/epg/search
    if (pathname === '/api/epg/search') {
      const q = (reqUrl.searchParams.get('q') || '').toLowerCase().trim();
      const now = new Date();
      const from = new Date(now.getTime() - 2 * 3600000);
      const to = new Date(now.getTime() + 5 * 86400000);

      let allEvents = [];
      MOCK_CHANNELS.forEach(c => {
        const evs = generateEPGEvents(c.id, from, to);
        evs.forEach(e => {
          const titleEn = (e.titles.eng || '').toLowerCase();
          const titleGr = (e.titles.ell || '').toLowerCase();
          const chName = c.name.toLowerCase();
          if (titleEn.includes(q) || titleGr.includes(q) || chName.includes(q)) {
            allEvents.push({
              ...e,
              channel_name: c.name,
              channel_number: c.number
            });
          }
        });
      });
      return new Response(JSON.stringify(allEvents.slice(0, 20)), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // G. Recordings: /api/recordings
    if (pathname === '/api/recordings') {
      if (!options || options.method === 'GET' || !options.method) {
        return new Response(JSON.stringify(getStoredRecordings()), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      }
      if (options && options.method === 'POST') {
        const recList = getStoredRecordings();
        try {
          const body = JSON.parse(options.body);
          const ch = MOCK_CHANNELS.find(c => c.id === body.channel_id) || MOCK_CHANNELS[0];
          const newRec = {
            id: Date.now(),
            channel_id: body.channel_id,
            channel_name: ch.name,
            event_id: body.event_id || 0,
            title: body.title || 'Scheduled Broadcast',
            description: body.description || '',
            start_time: body.start_time,
            end_time: body.end_time,
            duration: body.duration || 3600,
            status: 'scheduled',
            created_at: new Date().toISOString()
          };
          recList.unshift(newRec);
          saveStoredRecordings(recList);
          return new Response(JSON.stringify(newRec), {
            status: 201,
            headers: { 'Content-Type': 'application/json' }
          });
        } catch (_) {}
      }
    }

    // H. Instant Recording: /api/recordings/instant
    if (pathname === '/api/recordings/instant' && options && options.method === 'POST') {
      const recList = getStoredRecordings();
      const body = JSON.parse(options.body || '{}');
      const ch = MOCK_CHANNELS.find(c => c.id === body.channel_id) || MOCK_CHANNELS[0];
      const { nowShow } = getChannelNowNext(ch.id);
      const title = nowShow ? (nowShow.titles.eng || ch.name) : ch.name;

      const newRec = {
        id: Date.now(),
        channel_id: ch.id,
        channel_name: ch.name,
        title: title,
        start_time: new Date().toISOString(),
        end_time: new Date(Date.now() + 3600000).toISOString(),
        duration: 3600,
        status: 'recording',
        created_at: new Date().toISOString()
      };
      recList.unshift(newRec);
      saveStoredRecordings(recList);
      return new Response(JSON.stringify(newRec), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // I. Delete Recording: /api/recordings/{id}
    if (pathname.startsWith('/api/recordings/') && options && options.method === 'DELETE') {
      const id = parseInt(pathname.replace('/api/recordings/', ''), 10);
      let recList = getStoredRecordings();
      recList = recList.filter(r => r.id !== id);
      saveStoredRecordings(recList);
      return new Response(JSON.stringify({ status: 'deleted' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // J. Clear Failed Recordings: /api/recordings/clear-failed
    if (pathname === '/api/recordings/clear-failed') {
      let recList = getStoredRecordings();
      recList = recList.filter(r => r.status !== 'failed');
      saveStoredRecordings(recList);
      return new Response(JSON.stringify({ cleared: 0 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // K. Recordings Storage
    if (pathname === '/api/recordings/storage') {
      return new Response(JSON.stringify({
        total_recordings: getStoredRecordings().length,
        total_bytes: 4294967296,
        total_duration: 7200,
        free_bytes: 107374182400,
        total_disk_bytes: 250000000000
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // L. Settings
    if (pathname === '/api/settings') {
      if (options && options.method === 'POST') {
        return new Response(JSON.stringify({ status: 'ok' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' }
        });
      }
      return new Response(JSON.stringify({
        preferred_language: 'eng',
        transcoder_mode: 'auto',
        video_bitrate: '4500k',
        audio_codec: 'aac',
        rec_pre_padding: 2,
        rec_post_padding: 5
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // M. System Status (Required by initial load and Settings tab)
    if (pathname === '/api/status') {
      return new Response(JSON.stringify({
        tuners: MOCK_TUNERS,
        hardware: 'nvenc',
        server_time: new Date().toISOString(),
        default_lang: 'eng',
        epg_languages: ['eng', 'fra', 'deu', 'ell']
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // N. Tuners List & Admin
    if (pathname === '/api/tuners') {
      return new Response(JSON.stringify(MOCK_TUNERS), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // Tuner Toggle: /api/tuners/{id}/toggle
    const toggleMatch = pathname.match(/^\/api\/tuners\/(\d+)\/toggle$/);
    if (toggleMatch && options && options.method === 'POST') {
      const tid = parseInt(toggleMatch[1], 10);
      const tuner = MOCK_TUNERS.find(t => t.id === tid);
      if (tuner) {
        try {
          const body = options.body ? JSON.parse(options.body) : {};
          tuner.enabled = body.enabled !== undefined ? !!body.enabled : !tuner.enabled;
        } catch (_) {
          tuner.enabled = !tuner.enabled;
        }
        broadcastTunersUpdate();
      }
      return new Response(JSON.stringify({ status: 'updated', enabled: tuner ? tuner.enabled : true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // Tuner Force Release: /api/tuners/{id}/force-release
    const releaseMatch = pathname.match(/^\/api\/tuners\/(\d+)\/force-release$/);
    if (releaseMatch && options && options.method === 'POST') {
      const tid = parseInt(releaseMatch[1], 10);
      const tuner = MOCK_TUNERS.find(t => t.id === tid);
      if (tuner) {
        tuner.status = 'idle';
        tuner.current_frequency = 0;
        tuner.current_channel = '';
        tuner.active_channels = [];
        tuner.viewers = [];
        broadcastTunersUpdate();
      }
      return new Response(JSON.stringify({ status: 'released' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // Stop Recording: /api/recordings/{id}/stop or stop-and-tune
    const stopRecMatch = pathname.match(/^\/api\/recordings\/(\d+)\/(stop|stop-and-tune)$/);
    if (stopRecMatch) {
      const recId = parseInt(stopRecMatch[1], 10);
      let recList = getStoredRecordings();
      const rec = recList.find(r => r.id === recId);
      if (rec) {
        rec.status = 'completed';
        saveStoredRecordings(recList);
      }
      MOCK_TUNERS.forEach(t => {
        if (t.active_recording && t.active_recording.recording_id === recId) {
          t.active_recording = null;
        }
      });
      broadcastTunersUpdate();
      return new Response(JSON.stringify({ status: 'stopped' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // Frequency Channel Scan: /api/scan & /api/scan/prune
    if (pathname === '/api/scan' && options && options.method === 'POST') {
      return new Response(JSON.stringify({
        new_channels: [],
        updated_channels: MOCK_CHANNELS,
        missing_channels: []
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    if (pathname === '/api/scan/prune') {
      return new Response(JSON.stringify({ status: 'ok' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // Reorder channels
    if (pathname === '/api/channels/reorder') {
      return new Response(JSON.stringify({ status: 'ok' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
      });
    }

    // Fallback to original fetch
    try {
      return await origFetch(url, options);
    } catch (e) {
      return new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
  };

  // ==========================================================
  // 3. MOCK WEBSOCKET SIMULATOR
  // ==========================================================
  class MockWebSocket {
    constructor(url) {
      this.url = url;
      this.readyState = 0; // CONNECTING
      this.onopen = null;
      this.onmessage = null;
      this.onclose = null;
      this.onerror = null;

      activeMockSockets.add(this);

      setTimeout(() => {
        this.readyState = 1; // OPEN
        if (this.onopen) this.onopen({ type: 'open' });

        // Initial Tuner status update with enabled tuners
        this._dispatch({
          event: 'tuners_update',
          tuners: MOCK_TUNERS
        });

        // Periodic signal updates (for real-time Signal Analyzer HUD)
        this._sigTimer = setInterval(() => {
          if (this.readyState === 1) {
            const jitter = (Math.random() - 0.5) * 0.4;
            this._dispatch({
              event: 'signal',
              signal: {
                lock: true,
                carrier: true,
                viterbi: true,
                sync: true,
                snr_db: Math.round((29.2 + jitter) * 10) / 10,
                quality_pct: Math.min(100, Math.round(95 + jitter * 2)),
                strength_pct: Math.min(100, Math.round(88 + jitter * 3)),
                ber: 0,
                unc: 0
              }
            });
          }
        }, 2000);
      }, 50);
    }

    send(data) {
      try {
        const msg = JSON.parse(data);
        if (msg.action === 'tune') {
          const ch = MOCK_CHANNELS.find(c => c.id === msg.channel_id) || MOCK_CHANNELS[0];
          activeMockChannel = ch;
          const { nowShow, nextShow } = getChannelNowNext(ch.id);

          // Find appropriate tuner:
          // 1. Same frequency?
          let targetTuner = MOCK_TUNERS.find(t => t.enabled && t.status === 'active' && t.current_frequency === ch.frequency);
          // 2. Idle tuner?
          if (!targetTuner) {
            targetTuner = MOCK_TUNERS.find(t => t.enabled && t.status === 'idle');
          }
          // 3. Fallback to any enabled tuner
          if (!targetTuner) {
            targetTuner = MOCK_TUNERS.find(t => t.enabled) || MOCK_TUNERS[0];
          }

          targetTuner.status = 'active';
          targetTuner.current_frequency = ch.frequency;
          targetTuner.current_channel = ch.name;
          targetTuner.active_channels = [ch.name];
          targetTuner.viewers = [
            {
              id: 'demo-session',
              ip: '127.0.0.1 (Demo User)',
              user_agent: navigator.userAgent,
              channel_id: ch.id,
              channel_name: ch.name,
              connected_at: new Date().toISOString()
            }
          ];

          broadcastTunersUpdate();

          setTimeout(() => {
            this._dispatch({
              event: 'tuned',
              tuner_id: targetTuner.id,
              channel: { ...ch, now_playing: nowShow, next_playing: nextShow },
              now_playing: nowShow,
              next_playing: nextShow,
              signal: {
                has_lock: true,
                has_signal: true,
                has_carrier: true,
                has_viterbi: true,
                has_sync: true,
                lock: true,
                carrier: true,
                viterbi: true,
                sync: true,
                snr_db: 29.5,
                quality_pct: 96,
                strength_pct: 89,
                ber: 0,
                unc: 0
              }
            });
          }, 100);
        }
      } catch (_) {}
    }

    close() {
      this.readyState = 3;
      activeMockSockets.delete(this);
      if (this._sigTimer) clearInterval(this._sigTimer);
      if (this.onclose) this.onclose({ type: 'close' });
    }

    _dispatch(data) {
      if (this.onmessage) {
        this.onmessage({ data: JSON.stringify(data) });
      }
    }
  }

  window.WebSocket = MockWebSocket;

  // ==========================================================
  // 4. MOCK BROADCAST VIDEO SURFACE GENERATOR
  // ==========================================================
  let canvasStream = null;
  let canvasEl = null;
  let activeMockChannel = MOCK_CHANNELS[0];

  function getMockCanvasStream() {
    if (canvasStream) return canvasStream;

    canvasEl = document.createElement('canvas');
    canvasEl.width = 1280;
    canvasEl.height = 720;
    const ctx = canvasEl.getContext('2d');

    let frame = 0;
    function renderLoop() {
      frame++;
      const timeStr = new Date().toLocaleTimeString();

      // Broadcast background pattern
      const grad = ctx.createLinearGradient(0, 0, 1280, 720);
      grad.addColorStop(0, '#0a0f1d');
      grad.addColorStop(0.5, '#111827');
      grad.addColorStop(1, '#0b1329');
      ctx.fillStyle = grad;
      ctx.fillRect(0, 0, 1280, 720);

      // Subtle moving wave
      ctx.strokeStyle = 'rgba(59, 130, 246, 0.25)';
      ctx.lineWidth = 2;
      ctx.beginPath();
      for (let x = 0; x < 1280; x += 10) {
        const y = 360 + Math.sin((x + frame * 4) * 0.015) * 35;
        if (x === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      }
      ctx.stroke();

      // SMPTE color bar preview strip at top
      const colors = ['#ffffff', '#ffff00', '#00ffff', '#00ff00', '#ff00ff', '#ff0000', '#0000ff'];
      const barW = 1280 / colors.length;
      for (let i = 0; i < colors.length; i++) {
        ctx.fillStyle = colors[i];
        ctx.fillRect(i * barW, 40, barW, 12);
      }

      // Channel Card
      ctx.fillStyle = '#ffffff';
      ctx.font = 'bold 44px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(`${activeMockChannel.number}. ${activeMockChannel.name}`, 640, 290);

      ctx.fillStyle = '#94a3b8';
      ctx.font = '22px sans-serif';
      ctx.fillText(`DVB-T2 Broadcast Feed • 506.0 MHz • 1080p50 • H.264 / AAC`, 640, 340);

      // Live clock
      ctx.fillStyle = '#38bdf8';
      ctx.font = 'bold 36px monospace';
      ctx.fillText(timeStr, 640, 420);

      // Status pill
      ctx.fillStyle = 'rgba(34, 197, 94, 0.2)';
      ctx.strokeStyle = '#22c55e';
      ctx.lineWidth = 1.5;
      ctx.beginPath();
      ctx.roundRect(500, 460, 280, 42, 21);
      ctx.fill();
      ctx.stroke();

      ctx.fillStyle = '#4ade80';
      ctx.font = 'bold 18px sans-serif';
      ctx.fillText('● LIVE BROADCAST (MOCK)', 640, 487);

      requestAnimationFrame(renderLoop);
    }
    renderLoop();

    // Attach silent Web Audio tone so HTML5 video player detects real audio track
    let audioTrack = null;
    try {
      const audioCtx = new (window.AudioContext || window.webkitAudioContext)();
      const osc = audioCtx.createOscillator();
      const gain = audioCtx.createGain();
      const dest = audioCtx.createMediaStreamDestination();
      osc.type = 'sine';
      osc.frequency.value = 440;
      gain.gain.value = 0.001; // virtually silent hum
      osc.connect(gain);
      gain.connect(dest);
      osc.start();
      audioTrack = dest.stream.getAudioTracks()[0];
    } catch (_) {}

    canvasStream = canvasEl.captureStream(30);
    if (audioTrack) {
      canvasStream.addTrack(audioTrack);
    }
    return canvasStream;
  }

  // Intercept mpegts createPlayer to attach live canvas stream seamlessly
  function hookMpegts() {
    if (!window.mpegts || window.mpegts._mockHooked) return;
    window.mpegts._mockHooked = true;
    const origCreatePlayer = window.mpegts.createPlayer;
    window.mpegts.createPlayer = function(mediaDataSource, config) {
      const isMock = isGitHubPages || isMockRequested;
      if (!isMock && origCreatePlayer) {
        return origCreatePlayer.call(window.mpegts, mediaDataSource, config);
      }

      let attachedMedia = null;
      const listeners = {};
      let isLoaded = false;

      return {
        attachMediaElement(el) {
          attachedMedia = el;
          const stream = getMockCanvasStream();
          el.srcObject = stream;
        },
        detachMediaElement() {
          if (attachedMedia) {
            attachedMedia.srcObject = null;
            attachedMedia = null;
          }
        },
        load() {
          isLoaded = true;
          setTimeout(() => {
            if (listeners['media_info']) {
              listeners['media_info'].forEach(fn => fn({}));
            }
          }, 60);
        },
        play() {
          if (attachedMedia) {
            return attachedMedia.play().catch(() => {});
          }
          return Promise.resolve();
        },
        pause() {
          if (attachedMedia) attachedMedia.pause();
        },
        unload() {
          if (attachedMedia) attachedMedia.srcObject = null;
        },
        destroy() {
          if (attachedMedia) {
            attachedMedia.srcObject = null;
            attachedMedia = null;
          }
        },
        on(event, fn) {
          if (!listeners[event]) listeners[event] = [];
          listeners[event].push(fn);
          if (event === 'media_info' && isLoaded) {
            setTimeout(() => fn({}), 10);
          }
        }
      };
    };
  }

  let _mpegts = window.mpegts;
  try {
    Object.defineProperty(window, 'mpegts', {
      configurable: true,
      enumerable: true,
      get() {
        return _mpegts;
      },
      set(val) {
        _mpegts = val;
        hookMpegts();
      }
    });
  } catch (_) {}
  if (_mpegts) hookMpegts();

  // ==========================================================
  // 5. BANNER INDICATOR FOR GITHUB PAGES DEMO
  // ==========================================================
  window.addEventListener('DOMContentLoaded', () => {
    const banner = document.createElement('div');
    banner.id = 'github-pages-demo-banner';
    banner.style.cssText = `
      position: fixed;
      top: 0;
      left: 0;
      right: 0;
      height: 32px;
      background: linear-gradient(90deg, #1e3a8a, #0284c7);
      color: #ffffff;
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0 1rem;
      font-size: 0.8rem;
      font-weight: 600;
      z-index: 999999;
      box-shadow: 0 2px 10px rgba(0,0,0,0.5);
    `;
    banner.innerHTML = `
      <div style="display:flex; align-items:center; gap:0.5rem;">
        <span>📺</span>
        <span><strong>Live Interactive Demo:</strong> Running in client-side simulated tuner mode on GitHub Pages</span>
      </div>
      <div style="display:flex; align-items:center; gap:1rem;">
        <a href="https://github.com/bkbilly/dvb-web" target="_blank" style="color:#e0f2fe; text-decoration:underline;">View GitHub Repository ↗</a>
        <button type="button" style="background:transparent; border:none; color:#ffffff; cursor:pointer; font-size:1.1rem; line-height:1;" onclick="this.parentElement.parentElement.remove(); document.body.style.paddingTop='0';">&times;</button>
      </div>
    `;
    document.body.appendChild(banner);
    document.body.style.paddingTop = '32px';
  });

})();
