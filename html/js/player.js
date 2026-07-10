let globalBandwidthEstimate = NaN;
let bandwidthObserverStarted = false;

function isPositiveFinite(value){
  return Number.isFinite(value) && value > 0;
}

function safePlay(video){
  video.play().catch(()=>{});
}

function safeStartLoad(hls){
  try{ hls.startLoad(-1); }catch(e){ /* ignore */ }
}

function cacheBustedUrl(url){
  const separator = url.includes('?') ? '&' : '?';
  return url + separator + '_jellych_live=' + Date.now();
}

function startBandwidthObserver(){
  if(bandwidthObserverStarted) return;
  bandwidthObserverStarted = true;
  if(!window.PerformanceObserver) return;
  try{
    const observer = new PerformanceObserver((list) => {
      const entries = list.getEntries();
      for(const entry of entries){
        if(entry.name && (entry.name.includes('.ts') || entry.name.includes('.m3u8'))){
          const transferSize = entry.transferSize || entry.encodedBodySize || entry.decodedBodySize;
          const duration = entry.duration; // ms
          if(transferSize > 0 && duration > 0){
            const bps = (transferSize * 8 * 1000) / duration;
            if(isPositiveFinite(bps)) globalBandwidthEstimate = bps;
          }
        }
      }
    });
    observer.observe({ type: 'resource', buffered: true });
  }catch(e){ /* ignore */ }
}

export class Player {
  constructor(video){
    this.video = video;
    this.hls = null;
    this.currentUrl = '';
    this.wantsPlayback = false;
    this.networkRecoveryTimer = null;
    startBandwidthObserver();
  }

  recoverNetworkError(hls){
    if(!this.wantsPlayback || this.hls !== hls || this.networkRecoveryTimer) return;
    try{ hls.stopLoad(); }catch(e){ /* ignore */ }
    this.networkRecoveryTimer = setTimeout(()=>{
      this.networkRecoveryTimer = null;
      if(!this.wantsPlayback || this.hls !== hls || !this.currentUrl) return;
      try{
        // Reloading the manifest resets a stale fragment position while keeping
        // the MediaSource attached, so recovery does not look like a user pause.
        hls.loadSource(cacheBustedUrl(this.currentUrl));
        safeStartLoad(hls);
        safePlay(this.video);
      }catch(e){ /* let the next error retry recovery */ }
    }, 500);
  }

  play(url){
    this.stop();
    this.currentUrl = url;
    this.wantsPlayback = true;
    if(this.video.canPlayType && this.video.canPlayType('application/vnd.apple.mpegurl')){
      this.video.src = url;
      safePlay(this.video);
      return;
    }

    if(window.Hls && Hls.isSupported()){
      const hls = new Hls({
        liveSyncDurationCount: 4,
        liveMaxLatencyDurationCount: 8,
        manifestLoadingMaxRetry: 10,
        manifestLoadingRetryDelay: 1000,
        levelLoadingMaxRetry: 10,
        levelLoadingRetryDelay: 1000,
        fragLoadingMaxRetry: 10,
        fragLoadingRetryDelay: 1000,
      });
      this.hls = hls;
      hls.loadSource(url);
      hls.attachMedia(this.video);
      hls.on(Hls.Events.MANIFEST_PARSED, ()=>safePlay(this.video));
      hls.on(Hls.Events.ERROR, (_, data)=>{
        if(!this.wantsPlayback || this.hls !== hls) return;
        if(!data) return;
        if(data.type === Hls.ErrorTypes.NETWORK_ERROR){
          const status = data.response && Number(data.response.code || data.response.status);
          if(data.fatal || status === 404) this.recoverNetworkError(hls);
          return;
        }
        if(!data.fatal) return;
        if(data.type === Hls.ErrorTypes.MEDIA_ERROR){
          try{ hls.recoverMediaError(); }catch(e){ /* ignore */ }
          safePlay(this.video);
        } else {
          this.recoverNetworkError(hls);
        }
      });
      return;
    }

    alert('No HLS support in this browser. Use Safari or enable Hls.js (script included).');
  }

  stop(){
    this.wantsPlayback = false;
    this.currentUrl = '';
    if(this.networkRecoveryTimer){ clearTimeout(this.networkRecoveryTimer); this.networkRecoveryTimer = null; }
    if(this.hls){ try{ this.hls.destroy(); }catch(e){} this.hls = null; }
    try{ this.video.pause(); }catch(e){}
    try{ this.video.removeAttribute('src'); this.video.load(); }catch(e){}
  }

  pause(){
    this.wantsPlayback = false;
    if(this.networkRecoveryTimer){ clearTimeout(this.networkRecoveryTimer); this.networkRecoveryTimer = null; }
  }

  notePlaybackWanted(){
    this.wantsPlayback = true;
    if(this.hls) safeStartLoad(this.hls);
  }

  resume(){
    if(this.wantsPlayback){
      if(this.hls) safeStartLoad(this.hls);
      safePlay(this.video);
    }
  }

  getBandwidthEstimate(){
    const candidates = [
      globalBandwidthEstimate,
      this.hls && this.hls.bandwidthEstimate,
      this.hls && this.hls.abrEwmaDefaultEstimate,
      this.hls && this.hls.abrController && this.hls.abrController.bandwidthEstimate,
    ];
    for(const value of candidates){
      if(isPositiveFinite(value)) return value;
    }
    return NaN;
  }
}
