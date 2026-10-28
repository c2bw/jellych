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
  const doc = (stage && stage.ownerDocument) || (typeof document !== 'undefined' ? document : null);
  let previousVolume = 1;

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

  const mediaEvents = ['loadstart', 'loadedmetadata', 'play', 'playing', 'pause', 'ended', 'emptied'];
  mediaEvents.forEach((eventName)=>{
    video.addEventListener(eventName, syncPlayback);
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

  function destroy(){
    if(doc) doc.removeEventListener('fullscreenchange', onFullscreenChange);
    mediaEvents.forEach((eventName)=>{
      video.removeEventListener(eventName, syncPlayback);
    });
    video.removeEventListener('volumechange', syncVolume);
  }

  return { destroy, setLowLatencyMode, syncPlayback, syncVolume };
}
