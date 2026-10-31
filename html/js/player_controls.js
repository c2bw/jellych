const LOW_LATENCY_STORAGE_KEY = 'jellych_low_latency';

function findControl(stage, controlName, fallbackId){
  if(stage && typeof stage.querySelector === 'function'){
    const scoped = stage.querySelector('[data-control="' + controlName + '"]');
    if(scoped) return scoped;
  }
  if(typeof document !== 'undefined' && fallbackId){
    return document.getElementById(fallbackId);
  }
  return null;
}

export function initPlayerControls({ video, stage, player }){
  const playToggle = findControl(stage, 'play-toggle', 'playerPlayToggle');
  const centerPlay = findControl(stage, 'center-play', 'playerCenterPlay');
  const muteToggle = findControl(stage, 'mute-toggle', 'playerMuteToggle');
  const volume = findControl(stage, 'volume-range', 'playerVolume');
  const pipToggle = findControl(stage, 'pip-toggle', 'playerPipToggle');
  const fullscreenToggle = findControl(stage, 'fullscreen-toggle', 'playerFullscreenToggle');
  const lowLatencyToggle = findControl(stage, 'low-latency-toggle', 'playerLowLatencyToggle');
  const liveBadge = findControl(stage, 'live-badge', 'playerLiveBadge');
  const liveBadgeText = findControl(stage, 'live-badge-text', null);
  const timeControls = findControl(stage, 'time-controls', 'playerTimeControls');
  const seekBack = findControl(stage, 'seek-back', null);
  const seekForward = findControl(stage, 'seek-forward', null);
  const seekRange = findControl(stage, 'seek-range', null);
  const doc = (stage && stage.ownerDocument) || (typeof document !== 'undefined' ? document : null);
  let previousVolume = 1;
  let isScrubbing = false;

  function hasSource(){
    return !!(video.currentSrc || video.src || (video.getAttribute && video.getAttribute('src')));
  }

  function requestPlay(){
    if(!hasSource()) return;
    video.play().catch(()=>{});
  }

  function togglePlayback(){
    if(video.paused) requestPlay();
    else video.pause();
  }

  function syncPlayback(){
    const ready = hasSource();
    const playing = ready && !video.paused && !video.ended;
    if(stage && stage.classList) stage.classList.toggle('is-playing', playing);
    if(playToggle){
      playToggle.disabled = !ready;
      playToggle.setAttribute('aria-label', playing ? 'Pause' : 'Play');
    }
    if(centerPlay) centerPlay.classList.toggle('hidden', !ready || playing);
  }

  function syncVolume(){
    const muted = video.muted || video.volume === 0;
    if(stage && stage.classList) stage.classList.toggle('is-muted', muted);
    if(muteToggle) muteToggle.setAttribute('aria-label', muted ? 'Unmute' : 'Mute');
    if(volume){
      volume.value = muted ? '0' : String(video.volume);
      if(volume.style && typeof volume.style.setProperty === 'function'){
        volume.style.setProperty('--volume-level', (Number(volume.value) * 100) + '%');
      }
    }
  }

  function toggleMute(){
    if(video.muted || video.volume === 0){
      video.muted = false;
      video.volume = previousVolume > 0 ? previousVolume : 1;
    }else{
      previousVolume = video.volume;
      video.muted = true;
    }
    syncVolume();
  }

  function syncTimeShift(){
    const ready = hasSource();
    const win = (ready && player && typeof player.getLiveSeekWindow === 'function')
      ? player.getLiveSeekWindow()
      : { minTime: 0, maxTime: 0, liveEdge: 0, currentTime: 0, windowSeconds: 0, behindLiveSeconds: 0, isLive: true };
    const hasWindow = ready && win.windowSeconds > 0.5;
    const behindSeconds = (ready && !win.isLive && win.behindLiveSeconds > 0.25)
      ? Math.max(1, Math.round(win.behindLiveSeconds))
      : 0;
    const isBehindLive = behindSeconds > 0;

    if(liveBadge){
      if(liveBadge.classList) liveBadge.classList.toggle('is-behind-live', isBehindLive);
      liveBadge.disabled = !ready || !isBehindLive;
      liveBadge.setAttribute('aria-label', isBehindLive ? `Behind live by ${behindSeconds} seconds. Jump to live edge` : 'Live edge');
      liveBadge.title = isBehindLive ? 'Jump to live edge' : 'At live edge';
    }
    if(liveBadgeText){
      liveBadgeText.textContent = isBehindLive ? `-${behindSeconds}s · Live` : 'Live';
    }
    if(seekBack){
      seekBack.disabled = !hasWindow || (win.behindLiveSeconds >= win.windowSeconds - 0.1);
    }
    if(seekForward){
      seekForward.disabled = !hasWindow || !isBehindLive;
    }
    if(seekRange){
      seekRange.disabled = !hasWindow;
      const maxWindow = hasWindow ? Math.max(1, Math.ceil(win.windowSeconds * 2) / 2) : 30;
      seekRange.min = String(-maxWindow);
      seekRange.max = '0';
      seekRange.step = '0.5';
      if(!isScrubbing){
        const clampedBehind = hasWindow ? Math.min(maxWindow, Math.max(0, win.behindLiveSeconds)) : 0;
        const sliderVal = isBehindLive ? -Math.round(clampedBehind * 2) / 2 : 0;
        seekRange.value = String(sliderVal);
      }
      const currentVal = Number(seekRange.value) || 0;
      const progressPct = maxWindow > 0 ? Math.min(100, Math.max(0, ((maxWindow + currentVal) / maxWindow) * 100)) : 100;
      if(seekRange.style && typeof seekRange.style.setProperty === 'function'){
        seekRange.style.setProperty('--seek-progress', progressPct + '%');
      }
    }
  }

  function setLowLatencyMode(enabled, persist){
    if(!lowLatencyToggle || !player || typeof player.setLowLatencyMode !== 'function') return;
    player.setLowLatencyMode(enabled);
    lowLatencyToggle.setAttribute('aria-pressed', enabled ? 'true' : 'false');
    lowLatencyToggle.setAttribute('aria-label', enabled ? 'Disable low latency mode' : 'Enable low latency mode');
    lowLatencyToggle.title = enabled
      ? 'Low latency enabled: approximately two seconds behind live'
      : 'Use an approximately two-second live buffer';
    if(persist){
      try{ window.localStorage.setItem(LOW_LATENCY_STORAGE_KEY, enabled ? 'true' : 'false'); }catch(e){ /* ignore */ }
    }
    syncTimeShift();
  }

  async function toggleFullscreen(){
    if(!doc) return;
    try{
      if(doc.fullscreenElement === stage) await doc.exitFullscreen();
      else if(stage && stage.requestFullscreen) await stage.requestFullscreen();
    }catch(e){ /* fullscreen may be unavailable */ }
  }

  async function togglePictureInPicture(){
    if(!doc || !doc.pictureInPictureEnabled || !video.requestPictureInPicture) return;
    try{
      if(doc.pictureInPictureElement === video) await doc.exitPictureInPicture();
      else if(hasSource()) await video.requestPictureInPicture();
    }catch(e){ /* PiP may be unavailable for the current stream */ }
  }

  function onFullscreenChange(){
    if(!doc || !stage) return;
    const fullscreen = doc.fullscreenElement === stage;
    stage.classList.toggle('is-fullscreen', fullscreen);
    if(fullscreenToggle){
      fullscreenToggle.setAttribute('aria-label', fullscreen ? 'Exit fullscreen' : 'Enter fullscreen');
    }
  }

  if(playToggle) playToggle.addEventListener('click', togglePlayback);
  if(centerPlay) centerPlay.addEventListener('click', togglePlayback);
  video.addEventListener('click', togglePlayback);
  video.addEventListener('dblclick', toggleFullscreen);
  if(muteToggle) muteToggle.addEventListener('click', toggleMute);
  if(volume){
    volume.addEventListener('input', ()=>{
      const nextVolume = Number(volume.value);
      video.volume = nextVolume;
      video.muted = nextVolume === 0;
      if(nextVolume > 0) previousVolume = nextVolume;
      syncVolume();
    });
  }
  if(pipToggle) pipToggle.addEventListener('click', togglePictureInPicture);
  if(fullscreenToggle) fullscreenToggle.addEventListener('click', toggleFullscreen);
  if(lowLatencyToggle){
    lowLatencyToggle.addEventListener('click', ()=>{
      setLowLatencyMode(!player.isLowLatencyMode(), true);
    });
  }
  if(liveBadge){
    liveBadge.addEventListener('click', ()=>{
      if(player && typeof player.seekToLive === 'function'){
        player.seekToLive();
        syncTimeShift();
      }
    });
  }
  if(seekBack){
    seekBack.addEventListener('click', ()=>{
      if(player && typeof player.seekBy === 'function'){
        player.seekBy(-1);
        syncTimeShift();
      }
    });
  }
  if(seekForward){
    seekForward.addEventListener('click', ()=>{
      if(player && typeof player.seekBy === 'function'){
        player.seekBy(1);
        syncTimeShift();
      }
    });
  }
  if(seekRange){
    seekRange.addEventListener('pointerdown', ()=>{ isScrubbing = true; });
    seekRange.addEventListener('pointerup', ()=>{ isScrubbing = false; syncTimeShift(); });
    seekRange.addEventListener('input', ()=>{
      const offset = Math.max(0, -Number(seekRange.value));
      if(player && typeof player.seekToLiveOffset === 'function'){
        player.seekToLiveOffset(offset);
      }
      syncTimeShift();
    });
    seekRange.addEventListener('change', ()=>{
      isScrubbing = false;
      const offset = Math.max(0, -Number(seekRange.value));
      if(player && typeof player.seekToLiveOffset === 'function'){
        player.seekToLiveOffset(offset);
      }
      syncTimeShift();
    });
  }

  function isTimeControlsVisible(){
    return !!(timeControls && (!timeControls.classList || !timeControls.classList.contains('hidden')));
  }

  if(stage){
    stage.addEventListener('keydown', (event)=>{
      if(event.target !== stage) return;
      const key = event.key.toLowerCase();
      if(key === ' ' || key === 'k'){
        event.preventDefault();
        togglePlayback();
      }else if(key === 'm'){
        event.preventDefault();
        toggleMute();
      }else if(key === 'f'){
        event.preventDefault();
        toggleFullscreen();
      }else if(key === 'arrowleft' && isTimeControlsVisible()){
        event.preventDefault();
        if(player && typeof player.seekBy === 'function'){
          player.seekBy(-1);
          syncTimeShift();
        }
      }else if(key === 'arrowright' && isTimeControlsVisible()){
        event.preventDefault();
        if(player && typeof player.seekBy === 'function'){
          player.seekBy(1);
          syncTimeShift();
        }
      }
    });
    stage.addEventListener('focusin', (event)=>{
      if(event.target && event.target.matches && event.target.matches(':focus-visible')) stage.classList.add('has-keyboard-focus');
    });
    stage.addEventListener('focusout', ()=>{
      window.setTimeout(()=>{
        if(doc && !stage.contains(doc.activeElement)) stage.classList.remove('has-keyboard-focus');
      }, 0);
    });
  }

  function syncPlaybackAndTime(){
    syncPlayback();
    syncTimeShift();
  }

  const mediaEvents = ['loadstart', 'loadedmetadata', 'play', 'playing', 'pause', 'ended', 'emptied'];
  mediaEvents.forEach((eventName)=>{
    video.addEventListener(eventName, syncPlaybackAndTime);
  });
  const timeEvents = ['timeupdate', 'progress', 'seeking', 'seeked'];
  timeEvents.forEach((eventName)=>{
    video.addEventListener(eventName, syncTimeShift);
  });
  video.addEventListener('volumechange', syncVolume);
  if(doc) doc.addEventListener('fullscreenchange', onFullscreenChange);

  if(pipToggle && (!doc || !doc.pictureInPictureEnabled || !video.requestPictureInPicture)){
    pipToggle.classList.add('hidden');
  }
  let lowLatencyEnabled = false;
  try{ lowLatencyEnabled = window.localStorage.getItem(LOW_LATENCY_STORAGE_KEY) === 'true'; }catch(e){ /* ignore */ }
  setLowLatencyMode(lowLatencyEnabled, false);
  syncPlayback();
  syncVolume();
  syncTimeShift();

  function destroy(){
    if(doc) doc.removeEventListener('fullscreenchange', onFullscreenChange);
    mediaEvents.forEach((eventName)=>{
      video.removeEventListener(eventName, syncPlaybackAndTime);
    });
    timeEvents.forEach((eventName)=>{
      video.removeEventListener(eventName, syncTimeShift);
    });
    video.removeEventListener('volumechange', syncVolume);
  }

  return { destroy, setLowLatencyMode, syncPlayback, syncVolume, syncTimeShift };
}
