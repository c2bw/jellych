import { formatTime, formatBandwidth } from './utils.js';

const HAVE_FUTURE_DATA = typeof HTMLMediaElement !== 'undefined' && Number.isFinite(HTMLMediaElement.HAVE_FUTURE_DATA)
  ? HTMLMediaElement.HAVE_FUTURE_DATA
  : 3;

export function formatFramePair(dropped, total){
  if(Number.isFinite(dropped) && Number.isFinite(total)) return dropped + '/' + total;
  return '-';
}

export function getBufferedAhead(video){
  if(!video) return 0;
  const currentTime = Number(video.currentTime) || 0;
  const buffered = video.buffered || { length: 0 };
  for(let i = 0; i < buffered.length; i++){
    if(currentTime >= buffered.start(i) && currentTime <= buffered.end(i)){
      return Math.max(0, buffered.end(i) - currentTime);
    }
  }
  return 0;
}

export function getDroppedFrameCounts(video){
  if(!video) return null;
  const quality = typeof video.getVideoPlaybackQuality === 'function' ? video.getVideoPlaybackQuality() : null;
  if(quality && Number.isFinite(quality.droppedVideoFrames) && Number.isFinite(quality.totalVideoFrames)){
    return {
      dropped: quality.droppedVideoFrames,
      total: quality.totalVideoFrames,
    };
  }
  if(Number.isFinite(video.webkitDroppedFrameCount) && Number.isFinite(video.webkitDecodedFrameCount)){
    return {
      dropped: video.webkitDroppedFrameCount,
      total: video.webkitDecodedFrameCount,
    };
  }
  return null;
}

function isVideoActive(video){
  if(!video) return false;
  return !!(
    (Number(video.readyState) > 0) ||
    video.currentSrc ||
    video.src ||
    (typeof video.getAttribute === 'function' && video.getAttribute('src'))
  );
}

export function aggregateStreamStats(entries){
  const list = Array.isArray(entries) ? entries : [];
  const active = list.filter((entry)=>entry && isVideoActive(entry.video));
  if(!active.length){
    return {
      activeCount: 0,
      stateText: 'Idle',
      items: [
        { label: 'Current', value: '-' },
        { label: 'Buffered', value: '-' },
        { label: 'Dropped frames', value: '-' },
        { label: 'Bandwidth', value: '-' },
      ],
    };
  }

  let maxCurrent = 0;
  let sumBuffered = 0;
  let sumDropped = 0;
  let sumTotalFrames = 0;
  let hasFrameStats = false;
  let sumBandwidth = 0;
  let hasBandwidth = false;
  let playingCount = 0;
  let bufferingCount = 0;

  for(const entry of active){
    const { video, player } = entry;
    const current = Number(video.currentTime) || 0;
    if(current > maxCurrent) maxCurrent = current;
    sumBuffered += getBufferedAhead(video);

    const frameCounts = getDroppedFrameCounts(video);
    if(frameCounts){
      sumDropped += frameCounts.dropped;
      sumTotalFrames += frameCounts.total;
      hasFrameStats = true;
    }

    const bw = player && typeof player.getBandwidthEstimate === 'function'
      ? Number(player.getBandwidthEstimate())
      : NaN;
    if(Number.isFinite(bw) && bw > 0){
      sumBandwidth += bw;
      hasBandwidth = true;
    }

    const readyState = Number(video.readyState) || 0;
    const waitingForData = readyState > 0 && readyState < HAVE_FUTURE_DATA;
    if(waitingForData){
      bufferingCount++;
    } else if(!video.paused){
      playingCount++;
    }
  }

  let baseState = 'Paused';
  if(active.length === 1){
    if(bufferingCount > 0) baseState = 'Buffering';
    else if(playingCount > 0) baseState = 'Playing';
  } else {
    if(playingCount > 0) baseState = 'Playing';
    else if(bufferingCount > 0) baseState = 'Buffering';
  }
  const stateText = active.length > 1 ? `${baseState} (${active.length})` : baseState;

  return {
    activeCount: active.length,
    stateText,
    items: [
      { label: 'Current', value: formatTime(maxCurrent) },
      { label: 'Buffered', value: formatTime(sumBuffered) },
      { label: 'Dropped frames', value: hasFrameStats ? formatFramePair(sumDropped, sumTotalFrames) : '-' },
      { label: 'Bandwidth', value: formatBandwidth(hasBandwidth ? sumBandwidth : NaN) },
    ],
  };
}

export function initStats({ video, player, getStreams, statsOverlay, statsState, statsGrid }){
  let timer = null;

  function renderCards(items){
    statsGrid.innerHTML = items.map(({label, value}) => `
      <div class="stat-tile">
        <div class="stat-label">${label}</div>
        <div class="stat-value">${value}</div>
      </div>
    `).join('');
  }

  function resolveEntries(){
    if(typeof getStreams === 'function'){
      const streams = getStreams();
      if(Array.isArray(streams)) return streams;
    }
    return video ? [{ video, player }] : [];
  }

  function renderIdle(){
    const idle = aggregateStreamStats([]);
    renderCards(idle.items);
    statsState.textContent = idle.stateText;
    statsOverlay.classList.remove('hidden');
  }

  function clearStats(){
    renderIdle();
  }

  function renderStats(items, stateText){
    renderCards(items);
    statsState.textContent = stateText;
    statsOverlay.classList.remove('hidden');
  }

  function updateStats(){
    const aggregated = aggregateStreamStats(resolveEntries());
    if(aggregated.activeCount === 0){
      clearStats();
      return;
    }
    renderStats(aggregated.items, aggregated.stateText);
  }

  function start(){ stop(); updateStats(); timer = setInterval(updateStats, 500); }
  function stop(){ if(timer){ clearInterval(timer); timer = null; } }

  renderIdle();

  return { start, stop, update: updateStats, clear: clearStats };
}
