import { channelNameOf } from './utils.js';
import { apiFetch, getControlSecret } from './auth.js';

export function initManager({ listEl, removeSelect, addBtn, newNameEl, removeBtn, managerMsgEl, player, stats, playerTitleEl }){
  const MIN_SEGMENTS_TO_PLAY = 4;
  const MAX_WAIT_MS = 45000;
  const POLL_MS = 250;
  const PLAY_HEARTBEAT_MS = 5000;

  const statusMeta = new Map();
  const statusDetails = new Map();
  const playCounts = new Map();
  const activeStreams = new Set();
  const channelEls = new Map();
  let playingChannel = null;
  let playHeartbeatId = null;
  let playTargetChannel = null;
  let suppressPauseHandling = false;
  const video = player && player.video ? player.video : null;

  function sleep(ms){ return new Promise(r => setTimeout(r, ms)); }

  async function waitForSegments(url, minSegments){
    const start = Date.now();
    let channel = '';
    try{
      const parsed = new URL(url, window.location.origin);
      const match = parsed.pathname.match(/^\/live\/([^/]+)\/index\.m3u8$/i);
      if(match) channel = decodeURIComponent(match[1]);
    }catch(e){ /* ignore */ }

    if(!channel) return false;

    const readyUrl = '/api/stream-ready/' + encodeURIComponent(channel) + '?minSegments=' + encodeURIComponent(String(minSegments));
    while(Date.now() - start < MAX_WAIT_MS){
      try{
        const check = await fetch(readyUrl, { cache: 'no-store' });
        if(check.ok){
          const payload = await check.json();
          if(payload && payload.ready === true) return true;
        }
      }catch(e){ /* ignore */ }
      await sleep(POLL_MS);
    }
    return false;
  }
  function clearList(){ listEl.innerHTML = ''; channelEls.clear(); }

  async function fetchJSON(url){
    try{
      const res = await fetch(url);
      if(!res.ok) return null;
      return await res.json();
    }catch(e){
      return null;
    }
  }

  function showManagerMsg(msg, isError){
    managerMsgEl.textContent = msg || '';
    managerMsgEl.className = isError ? 'form-message text-red-400' : 'form-message';
    if(msg){ setTimeout(()=>{ managerMsgEl.textContent = ''; }, 3500); }
  }

  function setStartState(channelName, started){
    const el = channelEls.get(channelName);
    if(!el || !el.startStop) return;
    el.startStop.textContent = started ? 'Stop' : 'Start';
    el.startStop.classList.toggle('btn-started', started);
  }

  function setPlaying(channelName, playing){
    for(const [key, el] of channelEls){
      if(!el.playPause) continue;
      if(key === channelName){
        el.playPause.textContent = playing ? 'Pause' : 'Play';
        el.playPause.classList.toggle('btn-playing', playing);
      } else {
        el.playPause.textContent = 'Play';
        el.playPause.classList.remove('btn-playing');
      }
    }
  }

  function setOfflineState(channelName, offline){
    const el = channelEls.get(channelName);
    if(!el) return;
    if(offline){
      if(el.startStop) el.startStop.classList.add('hidden');
      if(el.playPause) el.playPause.classList.add('hidden');
    } else {
      if(el.startStop) el.startStop.classList.remove('hidden');
      if(el.playPause) el.playPause.classList.remove('hidden');
    }
  }

  function normalizeChannelKey(channel){
    return channelNameOf(channel).toLowerCase();
  }

  function setControlsDisabled(startBtn, playPauseBtn, disabled){
    if(startBtn) startBtn.disabled = disabled;
    if(playPauseBtn) playPauseBtn.disabled = disabled;
  }

  function setPlayButtonState(button, playing){
    if(button == null) return;
    button.textContent = playing ? 'Pause' : 'Play';
    button.classList.toggle('btn-playing', playing);
  }

  function setPlayerTitle(channelName){
    if(!playerTitleEl) return;
    const titleTextEl = playerTitleEl.firstElementChild || playerTitleEl;
    const details = statusDetails.get(channelName) || {};
    const title = details.title || '';
    titleTextEl.textContent = title;
    playerTitleEl.classList.toggle('hidden', !title);
  }

  function clearPlayerTitle(){
    if(!playerTitleEl) return;
    const titleTextEl = playerTitleEl.firstElementChild || playerTitleEl;
    titleTextEl.textContent = '';
    playerTitleEl.classList.add('hidden');
  }

  function beginPlayback(channelName, url, playPauseBtn){
    setPlaying(channelName, true);
    setPlayButtonState(playPauseBtn, true);
    playTargetChannel = channelName;
    setPlayerTitle(channelName);
    suppressPauseHandling = true;
    try{
      player.play(url);
    } finally {
      setTimeout(()=>{ suppressPauseHandling = false; }, 0);
    }
    stats.start();
  }

  function recoverBufferedPlayback(){
    if(!playTargetChannel) return;
    startPlaybackTracking(playTargetChannel);
    player.resume();
  }

  function stopCurrentPlayback(channelName, clearStats){
    if(suppressPauseHandling) return;
    suppressPauseHandling = true;
    const targetChannel = channelName || playTargetChannel;
    try{
      player.stop();
      if(targetChannel) setPlaying(targetChannel, false);
      stats.stop();
      if(clearStats) stats.clear();
      playTargetChannel = null;
      clearPlayerTitle();
      stopPlaybackTracking();
    } finally {
      setTimeout(()=>{ suppressPauseHandling = false; }, 0);
    }
  }

  function formatViewers(count){
    if(!Number.isFinite(count) || count < 0) return '0';
    return new Intl.NumberFormat('en-US').format(count);
  }

  function getSessionId(){
    const key = 'jellych_session_id';
    let id = '';
    try{ id = localStorage.getItem(key) || ''; }catch(e){ /* ignore */ }
    if(!id){
      if(window.crypto && crypto.randomUUID){ id = crypto.randomUUID(); }
      else { id = Math.random().toString(36).slice(2) + Date.now().toString(36); }
      try{ localStorage.setItem(key, id); }catch(e){ /* ignore */ }
    }
    return id;
  }

  function sendPlayPing(channel, action){
    const sessionId = getSessionId();
    const payload = JSON.stringify({ sessionId, action });
    if(action === 'stop' && navigator.sendBeacon && !getControlSecret()){
      const blob = new Blob([payload], { type: 'application/json' });
      navigator.sendBeacon('/api/playing/' + encodeURIComponent(channel), blob);
      return;
    }
    apiFetch('/api/playing/' + encodeURIComponent(channel), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: payload,
      keepalive: action === 'stop',
    }).catch(()=>{});
  }

  function startPlaybackTracking(channel){
    if(!channel) return;
    if(playingChannel === channel && playHeartbeatId) return;
    stopPlaybackTracking();
    playingChannel = channel;
    sendPlayPing(channel, 'start');
    playHeartbeatId = setInterval(()=>sendPlayPing(channel, 'ping'), PLAY_HEARTBEAT_MS);
  }

  function stopPlaybackTracking(){
    if(!playingChannel) return;
    const channel = playingChannel;
    playingChannel = null;
    if(playHeartbeatId){ clearInterval(playHeartbeatId); playHeartbeatId = null; }
    sendPlayPing(channel, 'stop');
  }

  if(video){
    video.addEventListener('play', ()=>{
      if(playTargetChannel && player.notePlaybackWanted) player.notePlaybackWanted();
    });
    video.addEventListener('playing', ()=>{ if(playTargetChannel){ startPlaybackTracking(playTargetChannel); } });
    video.addEventListener('waiting', recoverBufferedPlayback);
    video.addEventListener('stalled', recoverBufferedPlayback);
    video.addEventListener('pause', ()=>{
      if(suppressPauseHandling) return;
      if(player.pause) player.pause();
      stopPlaybackTracking();
    });
    video.addEventListener('ended', ()=>{ stopPlaybackTracking(); });
    video.addEventListener('emptied', ()=>{ stopPlaybackTracking(); });
    window.addEventListener('beforeunload', ()=>{ stopPlaybackTracking(); });
  }

  function buildChannelRow(channelKey){
    const row = document.createElement('div');
    row.className = 'channel-row group';
    row.tabIndex = 0;
    row.dataset.channel = channelKey;
    return row;
  }

  function buildNameSection(channelName){
    const nameWrap = document.createElement('div');
    nameWrap.className = 'channel-name-wrap';

    const nameRow = document.createElement('div');
    nameRow.className = 'channel-name-line';

    const nameLink = document.createElement('a');
    nameLink.textContent = channelName;
    nameLink.href = 'https://twitch.tv/' + encodeURIComponent(channelName);
    nameLink.target = '_blank';
    nameLink.rel = 'noopener noreferrer';
    nameLink.className = 'channel-name';

    const metaText = document.createElement('div');
    metaText.className = 'streamer-meta';
    metaText.textContent = '';

    nameRow.appendChild(nameLink);
    nameWrap.appendChild(nameRow);
    nameWrap.appendChild(metaText);

    return { nameWrap, nameRow, metaText };
  }

  function buildPlayCount(){
    const playWrap = document.createElement('span');
    playWrap.className = 'play-count';

    const playDot = document.createElement('span');
    playDot.className = 'play-count-dot';

    const playValue = document.createElement('span');
    playValue.className = 'play-count-value';
    playValue.textContent = '0';

    playWrap.appendChild(playDot);
    playWrap.appendChild(playValue);
    return { playWrap, playValue };
  }

  function buildActionButtons(channelKey){
    const startStop = document.createElement('button');
    startStop.className = 'btn-base start-stop';
    startStop.textContent = 'Start';

    const playPause = document.createElement('button');
    playPause.className = 'btn-base play-pause';
    playPause.textContent = 'Play';
    playPause.disabled = true;

    startStop.onclick = ()=>{
      if(startStop.textContent === 'Start') startChannel(channelKey, startStop, playPause);
      else stopChannel(channelKey, startStop, playPause);
    };

    playPause.onclick = ()=>{
      if(playPause.textContent === 'Play'){
        const url = '/live/' + encodeURIComponent(channelKey) + '/index.m3u8';
        playWhenReady(channelKey, url, playPause);
      } else {
        stopCurrentPlayback(channelKey, false);
      }
    };

    return { startStop, playPause };
  }

  function makeButton(c){
    const channelName = channelNameOf(c);
    if(!channelName) return null;
    const channelKey = normalizeChannelKey(channelName);
    const row = buildChannelRow(channelKey);
    const { nameWrap, nameRow, metaText } = buildNameSection(channelName);
    const { playWrap, playValue } = buildPlayCount();
    const { startStop, playPause } = buildActionButtons(channelKey);

    nameRow.appendChild(playWrap);
    row.appendChild(nameWrap);
    row.appendChild(startStop);
    row.appendChild(playPause);
    channelEls.set(channelKey, { row, startStop, playPause, metaText, playWrap, playValue });
    return row;
  }

  function setChannelActive(key, isActive){
    const el = channelEls.get(key);
    if(!el) return;
    setStartState(key, isActive);
    if(el.playPause) el.playPause.disabled = !isActive;
  }

  function applyStatus(statusList){
    const ordered = Array.isArray(statusList) ? statusList : [];
    const statusMap = new Map();
    ordered.forEach((s)=>{
      const name = normalizeChannelKey(s);
      if(!name) return;
      const online = !!s.online;
      statusMap.set(name, online);
      const game = s.game || s.Game || '';
      const title = s.title || s.Title || '';
      const viewers = Number.isFinite(s.viewers) ? s.viewers : Number(s.Viewers);
      const parts = [formatViewers(viewers), game || 'Unknown'];
      const meta = online ? parts.join(' · ') : 'offline';
      statusMeta.set(name, meta);
      statusDetails.set(name, { title, game, viewers, online });
    });

    if(playTargetChannel) setPlayerTitle(playTargetChannel);

    for(const [key] of channelEls){
      if(statusMap.has(key)) setOfflineState(key, !statusMap.get(key));
    }

    if(!ordered.length) return;
    const frag = document.createDocumentFragment();
    const used = new Set();
    ordered.forEach((s)=>{
      const name = normalizeChannelKey(s);
      const el = channelEls.get(name);
      if(el && el.row){
        frag.appendChild(el.row);
        used.add(name);
      }
    });
    for(const [name, el] of channelEls){
      if(!used.has(name) && el.row) frag.appendChild(el.row);
    }
    listEl.textContent = '';
    listEl.appendChild(frag);
    updateAllMeta();
  }

  function applyPlayCounts(counts){
    playCounts.clear();
    if(counts && typeof counts === 'object'){
      for(const [name, value] of Object.entries(counts)){
        const key = normalizeChannelKey(name);
        const count = Number(value);
        if(key && Number.isFinite(count)) playCounts.set(key, count);
      }
    }
    updateAllMeta();
  }

  function applyActiveStreams(activeList){
    activeStreams.clear();
    if(Array.isArray(activeList)){
      activeList.forEach((name)=>{
        const key = normalizeChannelKey(name);
        if(key) activeStreams.add(key);
      });
    }
    updateAllMeta();
  }

  function syncActiveStreams(activeList){
    const active = Array.isArray(activeList) ? activeList : [];
    const activeSet = new Set(active.map(normalizeChannelKey).filter(Boolean));
    applyActiveStreams(active);
    for(const [key] of channelEls){
      setChannelActive(key, activeSet.has(key));
    }
  }

  async function refreshStatus(){
    const statuses = await fetchJSON('/api/status');
    if(statuses) applyStatus(statuses);
  }

  async function refreshPlayCounts(){
    const counts = await fetchJSON('/api/playing');
    if(counts) applyPlayCounts(counts);
  }

  async function refreshStreams(){
    const active = await fetchJSON('/api/streams');
    if(active) syncActiveStreams(active);
  }

  function updateAllMeta(){
    for(const [key, el] of channelEls){
      if(!el.metaText) continue;
      const base = statusMeta.get(key) || '';
      const playing = playCounts.get(key) || 0;
      el.metaText.textContent = base;
      if(el.playWrap && el.playValue){
        el.playValue.textContent = formatViewers(playing);
        if(activeStreams.has(key)) el.playWrap.classList.remove('hidden');
        else el.playWrap.classList.add('hidden');
      }
    }
  }

  async function startChannel(channelName, startBtn, playPauseBtn){
    const url = '/live/' + encodeURIComponent(channelName) + '/index.m3u8';
    setControlsDisabled(startBtn, playPauseBtn, true);
    try{
      const res = await apiFetch('/api/stream/' + encodeURIComponent(channelName), { method: 'POST' });
      if(res.ok || res.status === 409){
        setStartState(channelName, true);
        setControlsDisabled(startBtn, null, false);
        playPauseBtn.textContent = 'Buffering...';

        const ready = await waitForSegments(url, MIN_SEGMENTS_TO_PLAY);
        playPauseBtn.disabled = false;
        if(ready){
          beginPlayback(channelName, url, playPauseBtn);
        } else {
          playPauseBtn.textContent = 'Play';
          showManagerMsg('Stream is taking longer to buffer; press Play when ready.', false);
        }
        return;
      }
      const text = await res.text();
      throw new Error(text || res.statusText);
    }catch(err){
      setControlsDisabled(startBtn, playPauseBtn, false);
      console.error('start error', err);
      if(String(err).includes('already started')){
        setStartState(channelName, true);
        playWhenReady(channelName, url, playPauseBtn);
        return;
      }
      alert('Failed to start stream: ' + err);
    }
  }

  async function playWhenReady(channelName, url, playPauseBtn){
    playPauseBtn.disabled = true;
    playPauseBtn.textContent = 'Buffering...';
    const ready = await waitForSegments(url, MIN_SEGMENTS_TO_PLAY);
    playPauseBtn.disabled = false;
    if(!ready){
      playPauseBtn.textContent = 'Play';
      showManagerMsg('Not enough segments yet. Try again in a moment.', false);
      return;
    }
    beginPlayback(channelName, url, playPauseBtn);
  }

  async function stopChannel(channelName, startBtn, playPauseBtn){
    setControlsDisabled(startBtn, null, true);
    try{
      const res = await apiFetch('/api/stop/' + encodeURIComponent(channelName), { method: 'POST' });
      if(!res.ok){ const text = await res.text(); throw new Error(text || res.statusText); }
      const isCurrentPlayback = playTargetChannel === channelName;
      setStartState(channelName, false);
      setControlsDisabled(startBtn, null, false);
      if(isCurrentPlayback){
        stopCurrentPlayback(channelName, true);
      } else {
        setPlayButtonState(playPauseBtn, false);
      }
      if(playPauseBtn) playPauseBtn.disabled = true;
    }catch(err){ console.error('stop error', err); setControlsDisabled(startBtn, playPauseBtn, false); alert('Failed to stop stream: ' + err); }
  }

  function populateRemoveDropdown(channels){
    removeSelect.innerHTML = '';
    channels.forEach(c=>{
      const channelName = channelNameOf(c);
      if(!channelName) return;
      const opt = document.createElement('option'); opt.value = channelName; opt.textContent = channelName; removeSelect.appendChild(opt);
    });
  }

  function toChannelList(payload){
    return Array.isArray(payload) ? payload : [];
  }

  function buildChannelRows(channelList){
    const frag = document.createDocumentFragment();
    const rows = [];
    channelList.forEach((channel)=>{
      const row = makeButton(channel);
      if(!row) return;
      frag.appendChild(row);
      rows.push(row);
    });
    return { frag, rows };
  }

  function renderChannelList(channelList){
    clearList();
    const { frag, rows } = buildChannelRows(channelList);
    listEl.appendChild(frag);
    populateRemoveDropdown(channelList);
    return rows;
  }

  let hasInitializedChannelFocus = false;

  async function fetchChannels(){
    try{
      const response = await fetch('/api/channels');
      if(!response.ok) throw new Error('channels.json not found');
      const payload = await response.json();
      const channelList = toChannelList(payload);
      const rows = renderChannelList(channelList);
      await Promise.all([refreshStatus(), refreshPlayCounts(), refreshStreams()]);
      startPolling();
      if(!hasInitializedChannelFocus && rows.length){
        rows[0].focus();
        hasInitializedChannelFocus = true;
      }
    }catch(err){ listEl.textContent = 'Could not load channels.json - ' + err.message; }
  }

  // poll active stream state every 5s to keep UI in sync
  let pollId = null;
  function startPolling(){
    if(pollId) return;
    pollId = setInterval(async ()=>{
      await Promise.all([refreshStreams(), refreshStatus(), refreshPlayCounts()]);
    }, 5000);
  }

  addBtn.addEventListener('click', addChannel);
  async function addChannel(){
    const name = newNameEl.value.trim(); if(!name){ showManagerMsg('Name required', true); return; }
    const payload = { name };
    try{
      const res = await apiFetch('/api/channels/add', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(payload) });
      if(res.status === 201 || res.ok){ showManagerMsg('Channel added'); newNameEl.value = ''; fetchChannels(); return; }
      const text = await res.text(); throw new Error(text || res.statusText);
    }catch(e){ showManagerMsg('Add failed: '+e.message, true); }
  }

  removeBtn.addEventListener('click', removeChannel);
  async function removeChannel(){
    const name = removeSelect.value; if(!name){ showManagerMsg('Select a channel to remove', true); return; }
    try{
      const res = await apiFetch('/api/channels/remove', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({ name }) });
      if(res.ok){ showManagerMsg('Channel removed'); fetchChannels(); return; }
      const text = await res.text(); throw new Error(text || res.statusText);
    }catch(e){ showManagerMsg('Remove failed: '+e.message, true); }
  }

  return { fetchChannels };
}
