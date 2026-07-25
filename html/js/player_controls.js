const LOW_LATENCY_STORAGE_KEY = 'jellych_low_latency';

export function initPlayerControls({ video, stage, player }){
  const playToggle = document.getElementById('playerPlayToggle');
  const centerPlay = document.getElementById('playerCenterPlay');
  const muteToggle = document.getElementById('playerMuteToggle');
  const volume = document.getElementById('playerVolume');
  const pipToggle = document.getElementById('playerPipToggle');
  const fullscreenToggle = document.getElementById('playerFullscreenToggle');
  const lowLatencyToggle = document.getElementById('playerLowLatencyToggle');
  let previousVolume = 1;

  function hasSource(){
    return !!(video.currentSrc || video.src || video.getAttribute('src'));
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
    stage.classList.toggle('is-playing', playing);
    playToggle.disabled = !ready;
    playToggle.setAttribute('aria-label', playing ? 'Pause' : 'Play');
    centerPlay.classList.toggle('hidden', !ready || playing);
  }

  function syncVolume(){
    const muted = video.muted || video.volume === 0;
    stage.classList.toggle('is-muted', muted);
    muteToggle.setAttribute('aria-label', muted ? 'Unmute' : 'Mute');
    volume.value = muted ? '0' : String(video.volume);
    volume.style.setProperty('--volume-level', (Number(volume.value) * 100) + '%');
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
    try{
      if(document.fullscreenElement) await document.exitFullscreen();
      else await stage.requestFullscreen();
    }catch(e){ /* fullscreen may be unavailable */ }
  }

  async function togglePictureInPicture(){
    if(!document.pictureInPictureEnabled || !video.requestPictureInPicture) return;
    try{
      if(document.pictureInPictureElement) await document.exitPictureInPicture();
      else if(hasSource()) await video.requestPictureInPicture();
    }catch(e){ /* PiP may be unavailable for the current stream */ }
  }

  playToggle.addEventListener('click', togglePlayback);
  centerPlay.addEventListener('click', togglePlayback);
  video.addEventListener('click', togglePlayback);
  video.addEventListener('dblclick', toggleFullscreen);
  muteToggle.addEventListener('click', toggleMute);
  volume.addEventListener('input', ()=>{
    const nextVolume = Number(volume.value);
    video.volume = nextVolume;
    video.muted = nextVolume === 0;
    if(nextVolume > 0) previousVolume = nextVolume;
    syncVolume();
  });
  pipToggle.addEventListener('click', togglePictureInPicture);
  fullscreenToggle.addEventListener('click', toggleFullscreen);
  if(lowLatencyToggle){
    lowLatencyToggle.addEventListener('click', ()=>{
      setLowLatencyMode(!player.isLowLatencyMode(), true);
    });
  }

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
    if(event.target.matches && event.target.matches(':focus-visible')) stage.classList.add('has-keyboard-focus');
  });
  stage.addEventListener('focusout', ()=>{
    window.setTimeout(()=>{
      if(!stage.contains(document.activeElement)) stage.classList.remove('has-keyboard-focus');
    }, 0);
  });

  ['loadstart', 'loadedmetadata', 'play', 'playing', 'pause', 'ended', 'emptied'].forEach((eventName)=>{
    video.addEventListener(eventName, syncPlayback);
  });
  video.addEventListener('volumechange', syncVolume);
  document.addEventListener('fullscreenchange', ()=>{
    const fullscreen = document.fullscreenElement === stage;
    stage.classList.toggle('is-fullscreen', fullscreen);
    fullscreenToggle.setAttribute('aria-label', fullscreen ? 'Exit fullscreen' : 'Enter fullscreen');
  });

  if(!document.pictureInPictureEnabled || !video.requestPictureInPicture) pipToggle.classList.add('hidden');
  let lowLatencyEnabled = false;
  try{ lowLatencyEnabled = window.localStorage.getItem(LOW_LATENCY_STORAGE_KEY) === 'true'; }catch(e){ /* ignore */ }
  setLowLatencyMode(lowLatencyEnabled, false);
  syncPlayback();
  syncVolume();
}
