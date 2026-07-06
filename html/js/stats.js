import { formatTime, formatBandwidth } from './utils.js';

export function initStats({ video, player, statsOverlay, statsState, statsGrid }){
  let timer = null;

  function renderCards(items){
    statsGrid.innerHTML = items.map(({label, value}) => `
      <div class="rounded-md border border-white/10 bg-white/5 px-2 py-1.5">
        <div class="text-[10px] uppercase tracking-[0.14em] text-white/45">${label}</div>
        <div class="mt-0.5 font-medium text-white/90">${value}</div>
      </div>
    `).join('');
  }

  function renderIdle(){
    renderCards([
      { label: 'Current', value: '-' },
      { label: 'Buffered', value: '-' },
      { label: 'Dropped frames', value: '-' },
      { label: 'Bandwidth', value: '-' },
    ]);
    statsState.textContent = 'Idle';
    statsOverlay.classList.remove('hidden');
  }

  function formatFramePair(dropped, total){
    if(Number.isFinite(dropped) && Number.isFinite(total)) return dropped + '/' + total;
    return '-';
  }

  function clearStats(){
    renderIdle();
  }

  function renderStats(items, stateText){
    renderCards(items);
    statsState.textContent = stateText;
    statsOverlay.classList.remove('hidden');
  }

  function getBufferedAhead(){
    const currentTime = video.currentTime || 0;
    const buffered = video.buffered || { length: 0 };
    for(let i = 0; i < buffered.length; i++){
      if(currentTime >= buffered.start(i) && currentTime <= buffered.end(i)){
        return buffered.end(i) - currentTime;
      }
    }
    return 0;
  }

  function getDroppedFrames(){
    const quality = video.getVideoPlaybackQuality ? video.getVideoPlaybackQuality() : null;
    if(quality) return formatFramePair(quality.droppedVideoFrames, quality.totalVideoFrames);
    return formatFramePair(video.webkitDroppedFrameCount, video.webkitDecodedFrameCount);
  }

  function updateStats(){
    if(video.readyState === 0 && !video.currentSrc && !video.src) { clearStats(); return; }
    const bandwidthEstimate = player ? player.getBandwidthEstimate() : NaN;
    const waitingForData = video.readyState > 0 && video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA;
    const items = [
      { label: 'Current', value: formatTime(video.currentTime || 0) },
      { label: 'Buffered', value: formatTime(getBufferedAhead()) },
      { label: 'Dropped frames', value: getDroppedFrames() },
      { label: 'Bandwidth', value: formatBandwidth(bandwidthEstimate) },
    ];
    renderStats(items, waitingForData ? 'Buffering' : (video.paused ? 'Paused' : 'Playing'));
  }

  function start(){ stop(); updateStats(); timer = setInterval(updateStats, 500); }
  function stop(){ if(timer){ clearInterval(timer); timer = null; } }

  renderIdle();

  return { start, stop, update: updateStats, clear: clearStats };
}
