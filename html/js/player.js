let globalBandwidthEstimate = NaN;
let bandwidthObserverStarted = false;

function isPositiveFinite(value){
  return Number.isFinite(value) && value > 0;
}

function safePlay(video){
  video.play().catch(()=>{});
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
    startBandwidthObserver();
  }

  play(url){
    this.stop();
    if(this.video.canPlayType && this.video.canPlayType('application/vnd.apple.mpegurl')){
      this.video.src = url;
      safePlay(this.video);
      return;
    }

    if(window.Hls && Hls.isSupported()){
      const hls = new Hls();
      this.hls = hls;
      hls.loadSource(url);
      hls.attachMedia(this.video);
      hls.on(Hls.Events.MANIFEST_PARSED, ()=>safePlay(this.video));
      return;
    }

    alert('No HLS support in this browser. Use Safari or enable Hls.js (script included).');
  }

  stop(){
    if(this.hls){ try{ this.hls.destroy(); }catch(e){} this.hls = null; }
    try{ this.video.pause(); }catch(e){}
    try{ this.video.removeAttribute('src'); this.video.load(); }catch(e){}
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
