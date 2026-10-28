import { channelNameOf } from './utils.js';
import { apiFetch, getControlSecret } from './auth.js';
import { createPollingLoop } from './polling.js';
import { Player } from './player.js';
import { initPlayerControls } from './player_controls.js';

const MULTI_LAYOUT_STORAGE_KEY = 'jellych_multi_layout';
const VALID_LAYOUTS = new Set(['auto', 'spotlight', 'columns', 'stacked']);

function createDynamicStageElement(doc, channelName){
  const stage = doc.createElement('div');
  stage.className = 'player-stage';
  stage.tabIndex = 0;
  stage.dataset.channel = channelName;
  stage.innerHTML = `
    <div class="player-title hidden" data-control="title-bar">
      <div class="truncate" data-control="title-text"></div>
      <div class="tile-actions hidden" data-control="tile-actions">
        <button type="button" class="tile-action-btn" data-tile-action="spotlight" title="Spotlight this stream" aria-label="Spotlight stream">★</button>
        <button type="button" class="tile-action-btn" data-tile-action="move-prev" title="Move tile earlier" aria-label="Move tile earlier">←</button>
        <button type="button" class="tile-action-btn" data-tile-action="move-next" title="Move tile later" aria-label="Move tile later">→</button>
        <button type="button" class="tile-action-btn tile-action-close" data-tile-action="close" title="Close stream tile" aria-label="Close stream tile">×</button>
      </div>
    </div>
    <video playsinline preload="metadata"></video>
    <div class="player-idle" aria-hidden="true">
      <img src="/html/assets/img/twitch_jellyfish_icon.svg" alt="" />
      <span>Loading ${channelName}…</span>
    </div>
    <button data-control="center-play" class="player-center-play hidden" type="button" aria-label="Play stream">
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5v14l11-7z" /></svg>
    </button>
    <div class="player-controls">
      <div class="player-controls-left">
        <button data-control="play-toggle" class="player-control-button" type="button" aria-label="Play" disabled>
          <svg class="player-icon-play" viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5v14l11-7z" /></svg>
          <svg class="player-icon-pause" viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5h3v14H8zM14 5h3v14h-3z" /></svg>
        </button>
        <span class="player-live-badge"><i></i> Live</span>
        <button data-control="low-latency-toggle" class="player-latency-toggle" type="button" aria-pressed="false" title="Use an approximately two-second live buffer">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m13 2-8 12h6l-1 8 9-13h-6z" /></svg>
          <span>Low latency</span>
        </button>
      </div>
      <div class="player-controls-right">
        <div class="player-volume">
          <button data-control="mute-toggle" class="player-control-button" type="button" aria-label="Mute">
            <svg class="player-icon-volume" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 9v6h4l5 4V5L9 9zm12.5.5a4 4 0 0 1 0 5M19.5 7a7 7 0 0 1 0 10" /></svg>
            <svg class="player-icon-muted" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 9v6h4l5 4V5L9 9zm12 1 4 4m0-4-4 4" /></svg>
          </button>
          <input data-control="volume-range" type="range" min="0" max="1" step="0.05" value="1" aria-label="Volume" />
        </div>
        <button data-control="pip-toggle" class="player-control-button" type="button" aria-label="Picture in picture" title="Picture in picture">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 5h16v14H4zM12 12h6v5h-6z" /></svg>
        </button>
        <button data-control="fullscreen-toggle" class="player-control-button" type="button" aria-label="Enter fullscreen" title="Fullscreen">
          <svg class="player-icon-expand" viewBox="0 0 24 24" aria-hidden="true"><path d="M8 3H3v5M16 3h5v5M8 21H3v-5M16 21h5v-5" /></svg>
          <svg class="player-icon-collapse" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 8h5V3M21 8h-5V3M3 16h5v5M21 16h-5v5" /></svg>
        </button>
      </div>
    </div>
  `;
  return stage;
}

export function initManager({
  listEl,
  removeSelect,
  addBtn,
  newNameEl,
  removeBtn,
  managerMsgEl,
  player,
  stats,
  playerTitleEl,
  playerStageEl,
  playerGridEl,
  multiToggleEl,
  multiToolbarEl,
  multiCountEl,
  createPlayer = (video)=>new Player(video),
  setupPlayerControls = initPlayerControls,
  onPlaybackChannelChange = ()=>{},
}){
  const DEFAULT_MIN_SEGMENTS_TO_PLAY = 4;
  const MAX_WAIT_MS = 45000;
  const POLL_MS = 250;
  const PLAY_HEARTBEAT_MS = 5000;

  const statusMeta = new Map();
  const statusDetails = new Map();
  const playCounts = new Map();
  const activeStreams = new Set();
  const channelEls = new Map();
  const activeTiles = new Map();
  const playHeartbeats = new Map();
  let tileOrder = [];
  let primaryStageOwner = null;
  let focusedChannel = null;
  let multiMode = false;
  let currentLayout = 'auto';
  try{
    const savedLayout = window.localStorage && window.localStorage.getItem(MULTI_LAYOUT_STORAGE_KEY);
    if(savedLayout && VALID_LAYOUTS.has(savedLayout)) currentLayout = savedLayout;
  }catch(e){ /* ignore */ }

  const primaryVideo = player && player.video ? player.video : null;
  const resolvedPrimaryStage = playerStageEl || (typeof document !== 'undefined' ? document.getElementById('playerStage') : null);
  const resolvedGridEl = playerGridEl || (typeof document !== 'undefined' ? document.getElementById('playerGrid') : null);
  const resolvedMultiToggle = multiToggleEl || (typeof document !== 'undefined' ? document.getElementById('multiToggle') : null);
  const resolvedMultiToolbar = multiToolbarEl || (typeof document !== 'undefined' ? document.getElementById('multiToolbar') : null);
  const resolvedMultiCount = multiCountEl || (typeof document !== 'undefined' ? document.getElementById('multiCount') : null);

  function sleep(ms){ return new Promise(r => setTimeout(r, ms)); }

  async function waitForSegments(url, getMinSegments){
    const start = Date.now();
    let channel = '';
    try{
      const parsed = new URL(url, window.location.origin);
      const match = parsed.pathname.match(/^\/live\/([^/]+)\/index\.m3u8$/i);
      if(match) channel = decodeURIComponent(match[1]);
    }catch(e){ /* ignore */ }

    if(!channel) return false;

    while(Date.now() - start < MAX_WAIT_MS){
      const minSegments = typeof getMinSegments === 'function' ? getMinSegments() : getMinSegments;
      const readyUrl = '/api/stream-ready/' + encodeURIComponent(channel) + '?minSegments=' + encodeURIComponent(String(minSegments));
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

  function minSegmentsToPlay(){
    if(player && typeof player.getStartupSegmentCount === 'function'){
      return player.getStartupSegmentCount();
    }
    return DEFAULT_MIN_SEGMENTS_TO_PLAY;
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

  function syncDirectoryPlayButtons(){
    for(const [key, el] of channelEls){
      if(!el.playPause) continue;
      const playing = activeTiles.has(key);
      el.playPause.textContent = playing ? 'Pause' : 'Play';
      el.playPause.classList.toggle('btn-playing', playing);
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

  function updateTileTitle(tile){
    if(!tile || !tile.titleBarEl) return;
    const details = statusDetails.get(tile.channel) || {};
    const rawTitle = details.title || '';
    const showMultiHeader = multiMode || activeTiles.size > 1;
    let displayTitle = rawTitle;
    if(showMultiHeader){
      displayTitle = rawTitle ? `${tile.channel} · ${rawTitle}` : tile.channel;
    } else if(!displayTitle && tile.isDirect){
      displayTitle = tile.channel;
    }
    if(tile.titleTextEl) tile.titleTextEl.textContent = displayTitle;
    if(tile.tileActionsEl) tile.tileActionsEl.classList.toggle('hidden', !showMultiHeader);
    tile.titleBarEl.classList.toggle('hidden', !displayTitle && !showMultiHeader);
  }

  function clearPrimaryTitle(){
    if(!playerTitleEl) return;
    const titleTextEl = playerTitleEl.querySelector ? (playerTitleEl.querySelector('[data-control="title-text"]') || playerTitleEl.firstElementChild || playerTitleEl) : (playerTitleEl.firstElementChild || playerTitleEl);
    if(titleTextEl) titleTextEl.textContent = '';
    const actionsEl = playerTitleEl.querySelector ? playerTitleEl.querySelector('[data-control="tile-actions"]') : null;
    if(actionsEl) actionsEl.classList.add('hidden');
    playerTitleEl.classList.add('hidden');
  }

  function setFocusedChannel(channelName){
    const normalized = channelName ? normalizeChannelKey(channelName) : null;
    const nextFocus = (normalized && activeTiles.has(normalized))
      ? normalized
      : (tileOrder[0] || null);
    const changed = focusedChannel !== nextFocus;
    focusedChannel = nextFocus;
    for(const [key, tile] of activeTiles){
      if(tile.stage && tile.stage.classList){
        tile.stage.classList.toggle('is-focused', (multiMode || activeTiles.size > 1) && key === focusedChannel);
      }
    }
    if(changed){
      onPlaybackChannelChange(focusedChannel);
    }
  }

  function syncGridLayout(){
    tileOrder = tileOrder.filter((ch)=>activeTiles.has(ch));
    for(const key of activeTiles.keys()){
      if(!tileOrder.includes(key)) tileOrder.push(key);
    }
    const count = tileOrder.length;
    const effectiveLayout = (multiMode || count > 1) ? currentLayout : 'auto';
    if(resolvedGridEl){
      resolvedGridEl.dataset.layout = effectiveLayout;
      resolvedGridEl.dataset.count = String(count);
      resolvedGridEl.classList.toggle('is-multi', multiMode || count > 1);
    }
    if(resolvedMultiToolbar){
      resolvedMultiToolbar.classList.toggle('hidden', !multiMode && count <= 1);
      const buttons = resolvedMultiToolbar.querySelectorAll ? resolvedMultiToolbar.querySelectorAll('[data-layout-option]') : [];
      buttons.forEach((btn)=>{
        const active = btn.getAttribute('data-layout-option') === currentLayout;
        btn.classList.toggle('is-active', active);
        btn.setAttribute('aria-pressed', active ? 'true' : 'false');
      });
    }
    if(resolvedMultiCount){
      resolvedMultiCount.textContent = count === 1 ? '1 stream' : `${count} streams`;
    }
    if(resolvedPrimaryStage && resolvedPrimaryStage.classList){
      resolvedPrimaryStage.classList.toggle('hidden', count > 0 && !primaryStageOwner);
    }

    tileOrder.forEach((channelKey, index)=>{
      const tile = activeTiles.get(channelKey);
      if(!tile) return;
      if(tile.stage){
        if(tile.stage.style) tile.stage.style.order = String(index);
        if(tile.stage.classList){
          tile.stage.classList.toggle('is-spotlight', effectiveLayout === 'spotlight' && index === 0 && count > 1);
          tile.stage.classList.toggle('is-focused', (multiMode || count > 1) && channelKey === focusedChannel);
        }
      }
      if(tile.movePrevBtn) tile.movePrevBtn.disabled = index === 0;
      if(tile.moveNextBtn) tile.moveNextBtn.disabled = index >= count - 1;
      if(tile.spotlightBtn){
        const isSpotlighted = effectiveLayout === 'spotlight' && index === 0 && count > 1;
        tile.spotlightBtn.classList.toggle('is-active', isSpotlighted);
        tile.spotlightBtn.setAttribute('aria-pressed', isSpotlighted ? 'true' : 'false');
      }
      updateTileTitle(tile);
    });
  }

  function setTilingLayout(layoutName, persist = true){
    if(!VALID_LAYOUTS.has(layoutName)) return;
    currentLayout = layoutName;
    if(persist){
      try{
        if(window.localStorage) window.localStorage.setItem(MULTI_LAYOUT_STORAGE_KEY, currentLayout);
      }catch(e){ /* ignore */ }
    }
    syncGridLayout();
  }

  function promoteTileToSpotlight(channelKey){
    if(!activeTiles.has(channelKey)) return;
    const index = tileOrder.indexOf(channelKey);
    if(index === 0 && currentLayout === 'spotlight'){
      setTilingLayout('auto', true);
      setFocusedChannel(channelKey);
      return;
    }
    if(index > 0){
      tileOrder.splice(index, 1);
      tileOrder.unshift(channelKey);
    }
    setTilingLayout('spotlight', true);
    setFocusedChannel(channelKey);
  }

  function moveTile(channelKey, delta){
    const index = tileOrder.indexOf(channelKey);
    if(index < 0) return;
    const targetIndex = index + delta;
    if(targetIndex < 0 || targetIndex >= tileOrder.length) return;
    const swap = tileOrder[targetIndex];
    tileOrder[targetIndex] = channelKey;
    tileOrder[index] = swap;
    syncGridLayout();
  }

  function wireStageTileInteractions(tile){
    const { stage } = tile;
    if(!stage || tile.interactionsWired) return;
    tile.interactionsWired = true;

    stage.addEventListener('pointerdown', ()=>{
      if(tile.channel && activeTiles.has(tile.channel)){
        setFocusedChannel(tile.channel);
      }
    });
    stage.addEventListener('focusin', ()=>{
      if(tile.channel && activeTiles.has(tile.channel)){
        setFocusedChannel(tile.channel);
      }
    });

    if(tile.spotlightBtn){
      tile.spotlightBtn.addEventListener('click', (event)=>{
        event.stopPropagation();
        if(tile.channel) promoteTileToSpotlight(tile.channel);
      });
    }
    if(tile.movePrevBtn){
      tile.movePrevBtn.addEventListener('click', (event)=>{
        event.stopPropagation();
        if(tile.channel) moveTile(tile.channel, -1);
      });
    }
    if(tile.moveNextBtn){
      tile.moveNextBtn.addEventListener('click', (event)=>{
        event.stopPropagation();
        if(tile.channel) moveTile(tile.channel, 1);
      });
    }
    if(tile.closeBtn){
      tile.closeBtn.addEventListener('click', (event)=>{
        event.stopPropagation();
        if(tile.channel) stopChannelPlayback(tile.channel, activeTiles.size <= 1);
      });
    }
  }

  function wireTileMediaEvents(tile){
    const { video: tileVideo } = tile;
    if(!tileVideo || tile.mediaEventsWired) return;
    tile.mediaEventsWired = true;

    tileVideo.addEventListener('play', ()=>{
      if(tile.channel && activeTiles.has(tile.channel) && tile.player && tile.player.notePlaybackWanted){
        tile.player.notePlaybackWanted();
      }
      stats.update();
    });
    tileVideo.addEventListener('playing', ()=>{
      if(tile.channel && activeTiles.has(tile.channel)){
        startPlaybackTracking(tile.channel);
      }
      stats.update();
    });
    tileVideo.addEventListener('waiting', ()=>{
      if(tile.channel) recoverBufferedPlayback(tile.channel);
      stats.update();
    });
    tileVideo.addEventListener('stalled', ()=>{
      if(tile.channel) recoverBufferedPlayback(tile.channel);
      stats.update();
    });
    tileVideo.addEventListener('pause', ()=>{
      if(tile.suppressPause) return;
      if(tile.player && tile.player.pause) tile.player.pause();
      if(tile.channel) stopPlaybackTracking(tile.channel);
      stats.update();
    });
    tileVideo.addEventListener('ended', ()=>{
      if(tile.channel) stopPlaybackTracking(tile.channel);
      stats.update();
    });
    tileVideo.addEventListener('emptied', ()=>{
      if(tile.channel) stopPlaybackTracking(tile.channel);
      stats.update();
    });
    tileVideo.addEventListener('timeupdate', ()=>stats.update());
    tileVideo.addEventListener('loadedmetadata', ()=>stats.update());
  }

  function resolveStageParts(stage, fallbackTitleEl){
    const titleBarEl = (stage && stage.querySelector && stage.querySelector('[data-control="title-bar"]')) || fallbackTitleEl || null;
    const titleTextEl = (titleBarEl && titleBarEl.querySelector && titleBarEl.querySelector('[data-control="title-text"]')) || (titleBarEl && titleBarEl.firstElementChild) || titleBarEl;
    const tileActionsEl = titleBarEl && titleBarEl.querySelector ? titleBarEl.querySelector('[data-control="tile-actions"]') : null;
    const spotlightBtn = titleBarEl && titleBarEl.querySelector ? titleBarEl.querySelector('[data-tile-action="spotlight"]') : null;
    const movePrevBtn = titleBarEl && titleBarEl.querySelector ? titleBarEl.querySelector('[data-tile-action="move-prev"]') : null;
    const moveNextBtn = titleBarEl && titleBarEl.querySelector ? titleBarEl.querySelector('[data-tile-action="move-next"]') : null;
    const closeBtn = titleBarEl && titleBarEl.querySelector ? titleBarEl.querySelector('[data-tile-action="close"]') : null;
    return { titleBarEl, titleTextEl, tileActionsEl, spotlightBtn, movePrevBtn, moveNextBtn, closeBtn };
  }

  const primaryTileProto = {
    isPrimaryStage: true,
    channel: null,
    isDirect: false,
    suppressPause: false,
    stage: resolvedPrimaryStage,
    video: primaryVideo,
    player,
    controls: null,
    ...resolveStageParts(resolvedPrimaryStage, playerTitleEl),
  };
  wireStageTileInteractions(primaryTileProto);
  wireTileMediaEvents(primaryTileProto);

  function acquireTileForChannel(channelName, isDirect){
    const existing = activeTiles.get(channelName);
    if(existing){
      existing.isDirect = !!isDirect;
      return existing;
    }

    if(!primaryStageOwner){
      primaryStageOwner = channelName;
      primaryTileProto.channel = channelName;
      primaryTileProto.isDirect = !!isDirect;
      if(resolvedPrimaryStage){
        resolvedPrimaryStage.dataset.channel = channelName;
        resolvedPrimaryStage.classList.remove('hidden');
      }
      if(primaryVideo){
        primaryVideo.muted = false;
        if(!(primaryVideo.volume > 0)) primaryVideo.volume = 1;
      }
      activeTiles.set(channelName, primaryTileProto);
      if(!tileOrder.includes(channelName)) tileOrder.push(channelName);
      return primaryTileProto;
    }

    const doc = (resolvedGridEl && resolvedGridEl.ownerDocument) || document;
    const stage = createDynamicStageElement(doc, channelName);
    const tileVideo = stage.querySelector('video');
    const tilePlayer = createPlayer(tileVideo);
    tileVideo.muted = false;
    tileVideo.volume = 1;
    if(resolvedGridEl) resolvedGridEl.appendChild(stage);
    const controls = setupPlayerControls({ video: tileVideo, stage, player: tilePlayer });
    const tile = {
      isPrimaryStage: false,
      channel: channelName,
      isDirect: !!isDirect,
      suppressPause: false,
      stage,
      video: tileVideo,
      player: tilePlayer,
      controls,
      ...resolveStageParts(stage, null),
    };
    wireStageTileInteractions(tile);
    wireTileMediaEvents(tile);
    activeTiles.set(channelName, tile);
    if(!tileOrder.includes(channelName)) tileOrder.push(channelName);
    return tile;
  }

  function clearViewQueryParam(){
    try{
      if(!window.location || !window.history || typeof window.history.replaceState !== 'function') return;
      const url = new URL(window.location.href);
      if(!url.searchParams.has('view')) return;
      url.searchParams.delete('view');
      window.history.replaceState(null, '', url.pathname + url.search + url.hash);
    }catch(e){ /* ignore */ }
  }

  function stopChannelPlayback(channelName, clearStatsIfEmpty = false){
    const tile = activeTiles.get(channelName);
    if(!tile) return;
    tile.suppressPause = true;
    try{
      tile.player.stop();
      stopPlaybackTracking(channelName);
      activeTiles.delete(channelName);
      tileOrder = tileOrder.filter((ch)=>ch !== channelName);

      if(tile.isPrimaryStage){
        primaryStageOwner = null;
        tile.channel = null;
        tile.isDirect = false;
        if(resolvedPrimaryStage){
          delete resolvedPrimaryStage.dataset.channel;
          resolvedPrimaryStage.classList.remove('is-focused', 'is-spotlight');
          if(resolvedPrimaryStage.style) resolvedPrimaryStage.style.order = '';
        }
        clearPrimaryTitle();
      } else {
        if(tile.controls && typeof tile.controls.destroy === 'function'){
          tile.controls.destroy();
        }
        if(tile.stage && typeof tile.stage.remove === 'function'){
          tile.stage.remove();
        }
      }

      syncDirectoryPlayButtons();
      if(focusedChannel === channelName){
        setFocusedChannel(tileOrder[0] || null);
      } else {
        setFocusedChannel(focusedChannel);
      }
      syncGridLayout();

      if(activeTiles.size === 0){
        stats.stop();
        if(clearStatsIfEmpty) stats.clear();
        else stats.update();
      } else {
        stats.update();
      }
    } finally {
      setTimeout(()=>{ tile.suppressPause = false; }, 0);
    }
  }

  function stopAllPlayback(clearStats){
    const channels = Array.from(activeTiles.keys());
    if(!channels.length){
      stats.stop();
      if(clearStats) stats.clear();
      clearPrimaryTitle();
      onPlaybackChannelChange(null);
      return;
    }
    channels.forEach((ch, idx)=>{
      stopChannelPlayback(ch, clearStats && idx === channels.length - 1);
    });
  }

  function beginPlayback(channelName, url, playPauseBtn, isDirect = false){
    if(!multiMode){
      for(const existingChannel of Array.from(activeTiles.keys())){
        if(existingChannel !== channelName){
          stopChannelPlayback(existingChannel, false);
        }
      }
    }
    stopPlaybackTracking(channelName);
    if(!isDirect) clearViewQueryParam();

    const tile = acquireTileForChannel(channelName, isDirect);
    syncDirectoryPlayButtons();
    if(playPauseBtn) setPlayButtonState(playPauseBtn, true);
    setFocusedChannel(channelName);
    syncGridLayout();

    tile.suppressPause = true;
    try{
      tile.player.play(url);
      onPlaybackChannelChange(focusedChannel);
    } finally {
      setTimeout(()=>{ tile.suppressPause = false; }, 0);
    }
    stats.start();
  }

  function playDirectChannel(channelName){
    const channelKey = normalizeChannelKey(channelName);
    if(!channelKey) return false;
    const url = '/api/live-proxy/' + encodeURIComponent(channelKey) + '/index.m3u8';
    beginPlayback(channelKey, url, null, true);
    return true;
  }

  function recoverBufferedPlayback(channelName){
    const target = channelName || focusedChannel;
    if(!target) return;
    const tile = activeTiles.get(target);
    if(!tile) return;
    startPlaybackTracking(target);
    tile.player.resume();
  }

  function setMultiMode(enabled){
    const next = !!enabled;
    if(multiMode === next) return;
    multiMode = next;
    if(resolvedMultiToggle){
      resolvedMultiToggle.setAttribute('aria-pressed', multiMode ? 'true' : 'false');
      resolvedMultiToggle.classList.toggle('is-active', multiMode);
    }
    if(!multiMode && activeTiles.size > 1){
      const keepChannel = (focusedChannel && activeTiles.has(focusedChannel))
        ? focusedChannel
        : tileOrder[0];
      for(const ch of Array.from(activeTiles.keys())){
        if(ch !== keepChannel){
          stopChannelPlayback(ch, false);
        }
      }
      setFocusedChannel(keepChannel);
    }
    syncGridLayout();
  }

  function getActiveStreams(){
    return tileOrder
      .map((ch)=>activeTiles.get(ch))
      .filter(Boolean)
      .map((tile)=>({
        channel: tile.channel,
        video: tile.video,
        player: tile.player,
      }));
  }

  if(resolvedMultiToggle){
    resolvedMultiToggle.addEventListener('click', ()=>{
      setMultiMode(!multiMode);
    });
  }

  if(resolvedMultiToolbar && resolvedMultiToolbar.querySelectorAll){
    resolvedMultiToolbar.querySelectorAll('[data-layout-option]').forEach((btn)=>{
      btn.addEventListener('click', ()=>{
        const option = btn.getAttribute('data-layout-option');
        if(option) setTilingLayout(option, true);
      });
    });
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
    const tile = activeTiles.get(channel);
    if(!tile || tile.isDirect) return;
    if(playHeartbeats.has(channel)) return;
    sendPlayPing(channel, 'start');
    const timerId = setInterval(()=>sendPlayPing(channel, 'ping'), PLAY_HEARTBEAT_MS);
    playHeartbeats.set(channel, timerId);
  }

  function stopPlaybackTracking(channel){
    if(channel){
      const timerId = playHeartbeats.get(channel);
      if(timerId === undefined) return;
      clearInterval(timerId);
      playHeartbeats.delete(channel);
      sendPlayPing(channel, 'stop');
      return;
    }
    for(const [ch, timerId] of playHeartbeats.entries()){
      clearInterval(timerId);
      playHeartbeats.delete(ch);
      sendPlayPing(ch, 'stop');
    }
  }

  if(typeof window !== 'undefined' && window.addEventListener){
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
        stopChannelPlayback(channelKey, activeTiles.size <= 1);
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

    for(const tile of activeTiles.values()){
      updateTileTitle(tile);
    }

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

        const ready = await waitForSegments(url, minSegmentsToPlay);
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
    const ready = await waitForSegments(url, minSegmentsToPlay);
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
      const isCurrentPlayback = activeTiles.has(channelName);
      setStartState(channelName, false);
      setControlsDisabled(startBtn, null, false);
      if(isCurrentPlayback){
        stopChannelPlayback(channelName, activeTiles.size <= 1);
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

  // Wait five seconds after each completed refresh before polling again.
  const startPolling = createPollingLoop(
    ()=>Promise.all([refreshStreams(), refreshStatus(), refreshPlayCounts()]),
    { delayMs: 5000, onError: (error)=>console.error('poll error', error) },
  );

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

  return {
    fetchChannels,
    playDirectChannel,
    getActiveStreams,
    setMultiMode,
    isMultiMode: ()=>multiMode,
    setTilingLayout,
    getTilingLayout: ()=>currentLayout,
    stopAllPlayback,
  };
}
