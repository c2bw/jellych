const NATIVE_RECOVERY_BASE_DELAY_MS = 500;
const NATIVE_RECOVERY_MAX_DELAY_MS = 8000;
const NATIVE_RECOVERY_MAX_ATTEMPTS = 5;
const MEDIA_ERR_NETWORK = 2;
const BITRATE_SAMPLE_WEIGHT = 0.25;
const STANDARD_STARTUP_SEGMENTS = 4;
const LOW_LATENCY_STARTUP_SEGMENTS = 2;

const STANDARD_HLS_BUFFER_CONFIG = Object.freeze({
  liveSyncDurationCount: 4,
  liveMaxLatencyDurationCount: 8,
  maxBufferLength: 30,
  maxMaxBufferLength: 600,
  backBufferLength: Infinity,
});

const LOW_LATENCY_HLS_BUFFER_CONFIG = Object.freeze({
  liveSyncDurationCount: 2,
  liveMaxLatencyDurationCount: 4,
  maxBufferLength: 2,
  maxMaxBufferLength: 4,
  backBufferLength: 0,
});

const TIME_SHIFT_HLS_BUFFER_CONFIG = Object.freeze({
  liveMaxLatencyDurationCount: Infinity,
  maxBufferLength: 30,
  maxMaxBufferLength: 600,
  backBufferLength: Infinity,
});

const LIVE_EDGE_SNAP_SECONDS = 0.25;

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

export class Player {
  constructor(video){
    this.video = video;
    this.hls = null;
    this.lowLatencyMode = false;
    this.timeShifted = false;
    this.currentUrl = '';
    this.wantsPlayback = false;
    this.networkRecoveryTimer = null;
    this.usingNativeHls = false;
    this.nativeRecoveryTimer = null;
    this.nativeRecoveryAttempts = 0;
    this.bandwidthEstimate = NaN;
    this.video.addEventListener('error', ()=>this.recoverNativeError());
    this.video.addEventListener('playing', ()=>{
      if(!this.usingNativeHls) return;
      this.clearNativeRecovery();
      this.nativeRecoveryAttempts = 0;
    });
  }

  getStartupSegmentCount(){
    return this.lowLatencyMode ? LOW_LATENCY_STARTUP_SEGMENTS : STANDARD_STARTUP_SEGMENTS;
  }

  isLowLatencyMode(){
    return this.lowLatencyMode;
  }

  isTimeShifted(){
    return this.timeShifted;
  }

  applyBufferConfig(){
    if(!this.hls || !this.hls.config) return;
    Object.assign(
      this.hls.config,
      this.lowLatencyMode ? LOW_LATENCY_HLS_BUFFER_CONFIG : STANDARD_HLS_BUFFER_CONFIG,
    );
    if(this.timeShifted){
      Object.assign(this.hls.config, TIME_SHIFT_HLS_BUFFER_CONFIG);
    }
  }

  setTimeShifted(enabled){
    const next = !!enabled;
    if(this.timeShifted === next) return;
    this.timeShifted = next;
    this.applyBufferConfig();
  }

  setLowLatencyMode(enabled){
    this.lowLatencyMode = !!enabled;
    if(this.lowLatencyMode){
      this.timeShifted = false;
    }
    if(!this.hls || !this.hls.config) return;

    this.applyBufferConfig();
    if(this.lowLatencyMode){
      const liveSyncPosition = Number(this.hls.liveSyncPosition);
      const currentTime = Number(this.video.currentTime);
      if(Number.isFinite(liveSyncPosition) && Number.isFinite(currentTime) && liveSyncPosition > currentTime){
        try{ this.video.currentTime = liveSyncPosition; }catch(e){ /* wait for the next live sync update */ }
      }
    }
  }

  getLiveSeekWindow(){
    let minTime = NaN;
    let maxTime = NaN;

    const details = (this.hls && this.hls.latestLevelDetails) ||
      (this.hls && Array.isArray(this.hls.levels) && this.hls.currentLevel >= 0 && this.hls.levels[this.hls.currentLevel]
        ? this.hls.levels[this.hls.currentLevel].details
        : null);
    if(details && Array.isArray(details.fragments) && details.fragments.length > 0){
      const first = details.fragments[0];
      const last = details.fragments[details.fragments.length - 1];
      const start = Number(first && first.start);
      const end = Number(last && (last.end !== undefined ? last.end : (Number(last.start) + Number(last.duration || 0))));
      if(Number.isFinite(start) && Number.isFinite(end) && end >= start){
        minTime = start;
        maxTime = end;
      }
    }

    const seekable = this.video && this.video.seekable;
    if(seekable && Number(seekable.length) > 0){
      try{
        const sStart = Number(seekable.start(0));
        const sEnd = Number(seekable.end(seekable.length - 1));
        if(Number.isFinite(sStart) && Number.isFinite(sEnd) && sEnd >= sStart){
          minTime = Number.isFinite(minTime) ? Math.min(minTime, sStart) : sStart;
          maxTime = Number.isFinite(maxTime) ? Math.max(maxTime, sEnd) : sEnd;
        }
      }catch(e){ /* ignore invalid TimeRanges index */ }
    }

    const buffered = this.video && this.video.buffered;
    if(!Number.isFinite(minTime) && buffered && Number(buffered.length) > 0){
      try{
        const bStart = Number(buffered.start(0));
        const bEnd = Number(buffered.end(buffered.length - 1));
        if(Number.isFinite(bStart) && Number.isFinite(bEnd) && bEnd >= bStart){
          minTime = bStart;
          maxTime = bEnd;
        }
      }catch(e){ /* ignore invalid TimeRanges index */ }
    }

    const liveSync = Number(this.hls && this.hls.liveSyncPosition);
    if(!Number.isFinite(minTime)){
      minTime = 0;
      maxTime = isPositiveFinite(liveSync)
        ? liveSync
        : Math.max(0, Number(this.video && this.video.currentTime) || 0);
    } else if(isPositiveFinite(liveSync) && liveSync > maxTime){
      maxTime = liveSync;
    }

    let liveEdge = maxTime;
    if(isPositiveFinite(liveSync)){
      liveEdge = Math.min(maxTime, Math.max(minTime, liveSync));
    }

    const currentTime = Math.max(0, Number(this.video && this.video.currentTime) || 0);
    const windowSeconds = Math.max(0, liveEdge - minTime);
    const rawBehind = liveEdge - currentTime;
    const behindLiveSeconds = Number.isFinite(rawBehind)
      ? Math.max(0, Math.min(windowSeconds > 0 ? windowSeconds : Math.max(0, rawBehind), rawBehind))
      : 0;
    const isLive = !this.timeShifted || behindLiveSeconds <= LIVE_EDGE_SNAP_SECONDS;

    return {
      minTime,
      maxTime,
      liveEdge,
      currentTime,
      windowSeconds,
      behindLiveSeconds,
      isLive,
    };
  }

  emitTimeChange(){
    if(this.video && typeof this.video.dispatchEvent === 'function' && typeof Event === 'function'){
      try{ this.video.dispatchEvent(new Event('timeupdate')); }catch(e){ /* ignore */ }
    }
  }

  seekToLive(){
    const win = this.getLiveSeekWindow();
    this.setTimeShifted(false);
    if(this.video && Number.isFinite(win.liveEdge) && win.liveEdge >= 0){
      try{ this.video.currentTime = win.liveEdge; }catch(e){ /* ignore */ }
    }
    this.emitTimeChange();
    return win.liveEdge;
  }

  seekToLiveOffset(offsetSeconds){
    const requested = Number(offsetSeconds);
    if(!Number.isFinite(requested)) return this.getLiveSeekWindow();
    const win = this.getLiveSeekWindow();
    if(win.windowSeconds <= 0 || requested <= LIVE_EDGE_SNAP_SECONDS){
      this.seekToLive();
      return this.getLiveSeekWindow();
    }
    const clampedOffset = Math.min(win.windowSeconds, Math.max(0, requested));
    const targetTime = Math.max(win.minTime, win.liveEdge - clampedOffset);
    this.setTimeShifted(true);
    if(this.video){
      try{ this.video.currentTime = targetTime; }catch(e){ /* ignore */ }
    }
    this.emitTimeChange();
    return this.getLiveSeekWindow();
  }

  seekBy(deltaSeconds){
    const delta = Number(deltaSeconds);
    if(!Number.isFinite(delta) || delta === 0) return this.getLiveSeekWindow();
    const win = this.getLiveSeekWindow();
    const nextOffset = win.behindLiveSeconds - delta;
    return this.seekToLiveOffset(nextOffset);
  }

  recordFragmentBitrate(data){
    const frag = data && data.frag;
    const part = data && data.part;
    const stats = (data && data.stats) || (part && part.stats) || (frag && frag.stats);
    const duration = Number((part && part.duration) || (frag && frag.duration));
    const loaded = Number(stats && (stats.loaded || stats.total));
    const payloadBytes = Number(data && data.payload && data.payload.byteLength);
    const bytes = isPositiveFinite(loaded) ? loaded : payloadBytes;
    if(!isPositiveFinite(duration) || !isPositiveFinite(bytes)) return;

    const sample = (bytes * 8) / duration;
    if(!isPositiveFinite(sample)) return;
    this.bandwidthEstimate = isPositiveFinite(this.bandwidthEstimate)
      ? this.bandwidthEstimate * (1 - BITRATE_SAMPLE_WEIGHT) + sample * BITRATE_SAMPLE_WEIGHT
      : sample;
  }

  clearNativeRecovery(){
    if(this.nativeRecoveryTimer){
      clearTimeout(this.nativeRecoveryTimer);
      this.nativeRecoveryTimer = null;
    }
  }

  recoverNativeError(){
    if(!this.usingNativeHls || !this.wantsPlayback || !this.currentUrl || this.nativeRecoveryTimer) return;
    if(!this.video.error || this.video.error.code !== MEDIA_ERR_NETWORK) return;
    if(this.nativeRecoveryAttempts >= NATIVE_RECOVERY_MAX_ATTEMPTS) return;

    const delay = Math.min(
      NATIVE_RECOVERY_BASE_DELAY_MS * (2 ** this.nativeRecoveryAttempts),
      NATIVE_RECOVERY_MAX_DELAY_MS,
    );
    this.nativeRecoveryAttempts++;
    this.nativeRecoveryTimer = setTimeout(()=>{
      this.nativeRecoveryTimer = null;
      if(!this.usingNativeHls || !this.wantsPlayback || !this.currentUrl) return;
      try{
        this.video.src = cacheBustedUrl(this.currentUrl);
        this.video.load();
        safePlay(this.video);
      }catch(e){ /* let a subsequent media error retry recovery */ }
    }, delay);
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

    if(window.Hls && Hls.isSupported()){
      const hls = new Hls({
        ...(this.lowLatencyMode ? LOW_LATENCY_HLS_BUFFER_CONFIG : STANDARD_HLS_BUFFER_CONFIG),
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
      hls.on(Hls.Events.FRAG_BUFFERED, (_, data)=>{
        if(this.hls === hls) this.recordFragmentBitrate(data);
      });
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

    if(this.video.canPlayType && this.video.canPlayType('application/vnd.apple.mpegurl')){
      this.usingNativeHls = true;
      this.nativeRecoveryAttempts = 0;
      this.video.src = url;
      safePlay(this.video);
      return;
    }

    alert('No HLS support in this browser. Use Safari or enable Hls.js (script included).');
  }

  stop(){
    this.wantsPlayback = false;
    this.timeShifted = false;
    this.currentUrl = '';
    this.usingNativeHls = false;
    this.nativeRecoveryAttempts = 0;
    this.bandwidthEstimate = NaN;
    this.clearNativeRecovery();
    if(this.networkRecoveryTimer){ clearTimeout(this.networkRecoveryTimer); this.networkRecoveryTimer = null; }
    if(this.hls){ try{ this.hls.destroy(); }catch(e){} this.hls = null; }
    try{ this.video.pause(); }catch(e){}
    try{ this.video.removeAttribute('src'); this.video.load(); }catch(e){}
  }

  pause(){
    this.wantsPlayback = false;
    this.clearNativeRecovery();
    if(this.networkRecoveryTimer){ clearTimeout(this.networkRecoveryTimer); this.networkRecoveryTimer = null; }
  }

  notePlaybackWanted(){
    const shouldRestartLoad = !this.wantsPlayback;
    this.wantsPlayback = true;
    if(shouldRestartLoad && this.hls) safeStartLoad(this.hls);
  }

  resume(){
    if(this.wantsPlayback){
      // Hls.js schedules live-playlist reloads from EXT-X-TARGETDURATION.
      // Calling startLoad for every waiting/stalled event bypasses that
      // schedule and can create a manifest-request feedback loop.
      safePlay(this.video);
    }
  }

  getBandwidthEstimate(){
    return isPositiveFinite(this.bandwidthEstimate) ? this.bandwidthEstimate : NaN;
  }
}
