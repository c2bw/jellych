import { apiFetch } from './auth.js';
import { appendVODConversionControl, appendVODDownloadPresetBadge, formatVODDuration, formatVODMediaInfo, formatVODRemainingTime, vodConversionRequest, vodDownloadRequest, vodPlaybackPath, vodPresetCommand } from './vod_download.js';

const form = document.getElementById('vodForm');
const idInput = document.getElementById('vodId');
const addButton = document.getElementById('addVodBtn');
const msgEl = document.getElementById('vodMsg');
const listEl = document.getElementById('vodList');
const filterEl = document.getElementById('vodFilter');
const channelFilterEl = document.getElementById('vodChannelFilter');
const paginationEl = document.getElementById('vodPagination');
const previousPageEl = document.getElementById('vodPreviousPage');
const nextPageEl = document.getElementById('vodNextPage');
const pageStatusEl = document.getElementById('vodPageStatus');
const downloadPresetEl = document.getElementById('downloadPreset');
const downloadPresetCommandEl = document.getElementById('downloadPresetCommand');
let currentVODs = [];
let currentPage = 1;
let progressPollTimer = 0;
let resolvedVODPresetCommands = null;
const vodIDLength = 10;
const vodsPerPage = 15;

function updateDownloadPresetCommand(){
  if(!downloadPresetCommandEl) return;
  downloadPresetCommandEl.textContent = 'ffmpeg ' + vodPresetCommand(downloadPresetEl ? downloadPresetEl.value : 'original', resolvedVODPresetCommands);
}

async function loadVODPresetCommands(){
  try{
    resolvedVODPresetCommands = await fetchJSON('/api/vod-presets');
    updateDownloadPresetCommand();
  }catch{
    resolvedVODPresetCommands = null;
  }
}

function setMessage(message, isError){
  msgEl.textContent = message || '';
  msgEl.className = isError ? 'min-h-[20px] text-sm text-red-400' : 'min-h-[20px] text-sm text-white/50';
}

async function fetchJSON(url, options){
  const res = await apiFetch(url, options);
  if(!res.ok){
    const text = await res.text();
    throw new Error(text.trim() || res.statusText);
  }
  return await res.json();
}

function vodTitle(vod){
  return vod.title || vod.Title || vod.id || vod.ID || 'Untitled VOD';
}

function vodChannel(vod){
  return vod.channel || vod.Channel || '';
}

function vodID(vod){
  return vod.id || vod.ID || '';
}

function vodURL(vod){
  return vod.url || vod.URL || '';
}

function vodDate(vod){
  return vod.date || vod.Date || '';
}

function vodDownloaded(vod){
  return vod.downloaded || vod.Downloaded || false;
}

function vodDownloadActive(vod){
  return vod.downloadActive || vod.DownloadActive || false;
}

function vodDownloadSize(vod){
  const value = vod.downloadSize ?? vod.DownloadSize ?? vod.totalSize ?? vod.TotalSize ?? 0;
  const size = Number(value);
  if(!Number.isFinite(size) || size <= 0) return 0;
  return size;
}

function vodDownloadRate(vod){
  const value = vod.downloadRate ?? vod.DownloadRate ?? vod.bytesPerSecond ?? vod.BytesPerSecond ?? 0;
  const rate = Number(value);
  if(!Number.isFinite(rate) || rate <= 0) return 0;
  return rate;
}

function vodDownloadPreset(vod){
  return vod.downloadPreset || vod.DownloadPreset || vod.preset || vod.Preset || '';
}

function vodDuration(vod){
  return vod.duration || vod.Duration || '';
}

function vodDownloadSpeed(vod){
  return vod.downloadSpeed || vod.DownloadSpeed || vod.speed || vod.Speed || '';
}

function vodDownloadOperation(vod){
  return vod.downloadOperation || vod.DownloadOperation || vod.operation || vod.Operation || '';
}

function vodOriginalSize(vod){
  const value = Number(vod.originalSize ?? vod.OriginalSize ?? 0);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function vodOriginalMediaInfo(vod){
  const codec = String(vod.downloadVideoCodec || vod.DownloadVideoCodec || vod.videoCodec || vod.VideoCodec || '').toLowerCase();
  const height = Number(vod.downloadVideoHeight ?? vod.DownloadVideoHeight ?? vod.videoHeight ?? vod.VideoHeight ?? 0);
  const bitrate = Number(vod.downloadTotalBitrate ?? vod.DownloadTotalBitrate ?? vod.totalBitrate ?? vod.TotalBitrate ?? 0);
  return formatVODMediaInfo(codec, height, bitrate);
}

function vodDownloadETASeconds(vod){
  const value = Number(vod.downloadETASeconds ?? vod.DownloadETASeconds ?? vod.etaSeconds ?? vod.ETASeconds ?? 0);
  return Number.isFinite(value) && value > 0 ? Math.ceil(value) : 0;
}

function formatBytes(value){
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let size = value;
  let unit = 0;
  while(size >= 1024 && unit < units.length - 1){
    size /= 1024;
    unit += 1;
  }
  if(unit === 0) return String(size) + ' ' + units[unit];
  return size.toFixed(size >= 10 ? 1 : 2) + ' ' + units[unit];
}

function formatMegabytesPerSecond(value){
  return (value / (1024 * 1024)).toFixed(2) + ' MB/s';
}

function formatVODDate(value, fallback){
  if(!value) return fallback;
  const date = new Date(value);
  if(Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

function vodTime(vod){
  const date = new Date(vodDate(vod));
  if(Number.isNaN(date.getTime())) return 0;
  return date.getTime();
}

function sortVODsByDate(vods){
  return [...vods].sort((a, b)=>vodTime(b) - vodTime(a));
}

function renderEmpty(message){
  listEl.innerHTML = '';
  const empty = document.createElement('div');
  empty.className = 'rounded-md border border-dashed border-white/10 p-4 text-sm text-white/45';
  empty.textContent = message;
  listEl.appendChild(empty);
}

function renderVOD(vod){
  const id = vodID(vod);
  const titleText = vodTitle(vod);
  const channelText = vodChannel(vod);
  const fullTitle = channelText ? channelText + ' - ' + titleText : titleText;
  const row = document.createElement('div');
  row.className = 'min-w-0 rounded-md border border-white/10 bg-white/[0.03] p-3';

  const actions = document.createElement('div');
  actions.className = 'flex shrink-0 gap-2';

  const playlist = document.createElement('a');
  playlist.href = vodPlaybackPath(id, vodDownloaded(vod), vodDownloadActive(vod));
  playlist.setAttribute('aria-label', 'Play');
  playlist.title = 'Play';
  playlist.innerHTML = '<svg class="h-4 w-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M8 5v14l11-7z"></path></svg>';
  playlist.className = 'button no-underline';

  const downloadAction = document.createElement('button');
  downloadAction.type = 'button';
  if(vodDownloadActive(vod)){
    downloadAction.textContent = vodDownloadOperation(vod) === 'convert' ? 'Converting' : 'Downloading';
    downloadAction.className = 'button';
    downloadAction.disabled = true;
  }else if(vodDownloaded(vod)){
    downloadAction.textContent = 'Delete File';
    downloadAction.className = 'button button-danger';
    downloadAction.addEventListener('click', ()=>deleteDownloadedVOD(id, downloadAction));
  }else{
    downloadAction.textContent = 'Download';
    downloadAction.className = 'button';
    downloadAction.addEventListener('click', ()=>downloadVOD(id, downloadAction));
  }

  const remove = document.createElement('button');
  remove.type = 'button';
  remove.textContent = 'Remove';
  remove.className = 'button button-danger';
  remove.addEventListener('click', ()=>removeVOD(id, remove));

  actions.appendChild(playlist);
  actions.appendChild(downloadAction);
  actions.appendChild(remove);

  const info = document.createElement('div');
  info.className = 'min-w-0';

  const titleRow = document.createElement('div');
  titleRow.className = 'flex min-w-0 items-center gap-3';

  const titleWrap = document.createElement('div');
  titleWrap.className = 'flex min-w-0 flex-1 items-center gap-2';

  const downloadPreset = vodDownloadPreset(vod);
  const originalSize = vodOriginalSize(vod);

  if(channelText){
    const channelBadge = document.createElement('span');
    channelBadge.textContent = channelText;
    channelBadge.title = channelText;
    channelBadge.className = 'max-w-[160px] shrink-0 truncate rounded border border-white/10 bg-black/40 px-2 py-1 text-xs font-semibold text-white/70';
    titleWrap.appendChild(channelBadge);
  }

  const title = document.createElement('a');
  title.href = vodURL(vod);
  title.target = '_blank';
  title.rel = 'noopener noreferrer';
  title.textContent = titleText;
  title.title = fullTitle;
  title.className = 'block min-w-0 flex-1 truncate font-semibold text-white no-underline hover:text-[#4aa3ff]';
  titleWrap.appendChild(title);

  const meta = document.createElement('div');
  meta.className = 'mt-1 flex min-w-0 flex-col gap-1 text-xs text-white/45 sm:flex-row sm:items-center';
  const dateText = document.createElement('span');
  dateText.textContent = formatVODDate(vodDate(vod), id);
  const size = vodDownloadSize(vod);
  meta.appendChild(dateText);
  const duration = formatVODDuration(vodDuration(vod));
  if(duration){
    const durationSeparator = document.createElement('span');
    durationSeparator.className = 'hidden sm:inline';
    durationSeparator.textContent = '-';
    const durationText = document.createElement('span');
    durationText.className = 'shrink-0 tabular-nums text-white/55';
    durationText.textContent = duration;
    meta.appendChild(durationSeparator);
    meta.appendChild(durationText);
  }
  if(size > 0){
    const sizeSeparator = document.createElement('span');
    sizeSeparator.className = 'hidden sm:inline';
    sizeSeparator.textContent = '-';
    const sizeText = document.createElement('span');
    sizeText.className = 'shrink-0 tabular-nums text-white/55';
    sizeText.textContent = originalSize > 0 ? formatBytes(originalSize) + ' → ' + formatBytes(size) : formatBytes(size);
    if(originalSize > 0) sizeText.title = 'Original → converted';
    meta.appendChild(sizeSeparator);
    meta.appendChild(sizeText);
  }
  if(vodDownloaded(vod) && !vodDownloadActive(vod) && downloadPreset === 'original'){
    appendVODConversionControl(document, meta, {
      title: titleText,
      onConvert: (preset, button, select)=>convertVOD(id, preset, button, select),
    });
    const mediaInfo = vodOriginalMediaInfo(vod);
    if(mediaInfo){
      const mediaInfoLabel = document.createElement('span');
      mediaInfoLabel.className = 'shrink-0 text-xs text-white/55';
      mediaInfoLabel.textContent = mediaInfo;
      meta.appendChild(mediaInfoLabel);
    }
  }else{
    appendVODDownloadPresetBadge(document, meta, {
      downloaded: vodDownloaded(vod),
      active: vodDownloadActive(vod),
      preset: downloadPreset,
    });
  }
  titleRow.appendChild(actions);
  titleRow.appendChild(titleWrap);
  info.appendChild(titleRow);
  info.appendChild(meta);
  const deletionAt = vod.estimatedDeletionAt || vod.EstimatedDeletionAt || '';
  if(vodDownloaded(vod) && deletionAt){
    const deletionText = document.createElement('div');
    deletionText.className = 'mt-1 text-xs text-amber-300/70';
    deletionText.textContent = 'Estimated deletion: ' + formatVODDate(deletionAt, deletionAt);
    info.appendChild(deletionText);
  }
  if(vodDownloadActive(vod)){
    const rate = vodDownloadRate(vod);
    const speed = vodDownloadSpeed(vod);
    const etaSeconds = vodDownloadETASeconds(vod);
    const converting = vodDownloadOperation(vod) === 'convert';
    const downloadStatus = document.createElement('div');
    downloadStatus.className = 'mt-2 text-xs tabular-nums text-white/55';
    const parts = [converting ? 'Converting' : 'Downloading'];
    if(speed) parts.push(speed);
    if(etaSeconds > 0) parts.push(formatVODRemainingTime(etaSeconds));
    if(!converting && rate > 0) parts.push(formatMegabytesPerSecond(rate));
    if(size > 0) parts.push(formatBytes(size));
    downloadStatus.textContent = parts.join(' - ');
    info.appendChild(downloadStatus);
  }
  row.appendChild(info);
  return row;
}

function renderPagination(totalItems, totalPages){
  if(totalItems === 0 || totalPages <= 1){
    paginationEl.classList.add('hidden');
    paginationEl.classList.remove('flex');
    return;
  }
  paginationEl.classList.remove('hidden');
  paginationEl.classList.add('flex');
  previousPageEl.disabled = currentPage <= 1;
  nextPageEl.disabled = currentPage >= totalPages;
  const first = (currentPage - 1) * vodsPerPage + 1;
  const last = Math.min(currentPage * vodsPerPage, totalItems);
  pageStatusEl.textContent = first + '-' + last + ' of ' + totalItems + ' (page ' + currentPage + ' of ' + totalPages + ')';
}

function renderVODs(vods){
  listEl.innerHTML = '';
  if(!Array.isArray(vods) || vods.length === 0){
    const onlyDownloaded = filterEl && filterEl.value === 'downloaded';
    const channelSelected = channelFilterEl && channelFilterEl.value !== 'all';
    renderEmpty(onlyDownloaded || channelSelected ? 'No VODs match these filters.' : 'No VODs saved yet.');
    renderPagination(0, 0);
    return;
  }
  const sorted = sortVODsByDate(vods);
  const totalPages = Math.ceil(sorted.length / vodsPerPage);
  currentPage = Math.min(Math.max(currentPage, 1), totalPages);
  const pageStart = (currentPage - 1) * vodsPerPage;
  const frag = document.createDocumentFragment();
  sorted.slice(pageStart, pageStart + vodsPerPage).forEach((vod)=>frag.appendChild(renderVOD(vod)));
  listEl.appendChild(frag);
  renderPagination(sorted.length, totalPages);
}

function applyVODFilter(){
  const onlyDownloaded = filterEl && filterEl.value === 'downloaded';
  const selectedChannel = channelFilterEl ? channelFilterEl.value : 'all';
  const vods = currentVODs.filter((vod)=>{
    if(onlyDownloaded && !vodDownloaded(vod)) return false;
    return selectedChannel === 'all' || vodChannel(vod).toLocaleLowerCase() === selectedChannel;
  });
  renderVODs(vods);
}

function updateChannelFilter(){
  if(!channelFilterEl) return;
  const selected = channelFilterEl.value;
  const channels = [...new Set(currentVODs.map(vodChannel).filter(Boolean))]
    .sort((a, b)=>a.localeCompare(b, undefined, {sensitivity: 'base'}));
  channelFilterEl.replaceChildren(new Option('All channels', 'all'));
  channels.forEach((channel)=>channelFilterEl.add(new Option(channel, channel.toLocaleLowerCase())));
  if([...channelFilterEl.options].some((option)=>option.value === selected)) channelFilterEl.value = selected;
}

async function loadVODs(){
  try{
    currentVODs = await fetchJSON('/api/vods');
    updateChannelFilter();
    applyVODFilter();
    scheduleProgressPolling();
  }catch(err){
    listEl.textContent = 'Could not load VODs: ' + err.message;
  }
}

function scheduleProgressPolling(){
  if(progressPollTimer){
    clearTimeout(progressPollTimer);
    progressPollTimer = 0;
  }
  if(!currentVODs.some(vodDownloadActive)) return;
  progressPollTimer = window.setTimeout(async ()=>{
    progressPollTimer = 0;
    await loadVODs();
  }, 2000);
}

async function addVOD(event){
  event.preventDefault();
  const id = idInput.value.trim();
  if(id.length !== vodIDLength || !/^\d+$/.test(id)){
    setMessage('VOD ID must be ' + vodIDLength + ' digits', true);
    return;
  }

  addButton.disabled = true;
  setMessage('Adding VOD...', false);
  try{
    await fetchJSON('/api/vods', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id }),
    });
    idInput.value = '';
    setMessage('VOD added', false);
    await loadVODs();
  }catch(err){
    setMessage('Add failed: ' + err.message, true);
  }finally{
    addButton.disabled = false;
  }
}

async function removeVOD(id, button){
  if(!id) return;
  button.disabled = true;
  setMessage('Removing VOD...', false);
  try{
    const res = await apiFetch('/api/vods/' + encodeURIComponent(id), { method: 'DELETE' });
    if(!res.ok){
      const text = await res.text();
      throw new Error(text.trim() || res.statusText);
    }
    setMessage('VOD removed', false);
    await loadVODs();
  }catch(err){
    setMessage('Remove failed: ' + err.message, true);
    button.disabled = false;
  }
}

async function downloadVOD(id, button){
  if(!id) return;
  button.disabled = true;
  setMessage('Starting VOD download...', false);
  try{
    const preset = downloadPresetEl ? downloadPresetEl.value : 'original';
    const res = await apiFetch('/api/vods/' + encodeURIComponent(id) + '/download', vodDownloadRequest(preset));
    if(!res.ok){
      const text = await res.text();
      throw new Error(text.trim() || res.statusText);
    }
    setMessage('Download started for VOD ' + id, false);
    currentVODs = currentVODs.map((vod)=>{
      if(vodID(vod) !== id) return vod;
      return {...vod, downloadActive: true};
    });
    applyVODFilter();
    scheduleProgressPolling();
  }catch(err){
    setMessage('Download failed: ' + err.message, true);
    button.disabled = false;
  }
}

async function convertVOD(id, preset, button, select){
  if(!id || preset === 'original') return;
  button.disabled = true;
  select.disabled = true;
  setMessage('Starting VOD conversion...', false);
  try{
    const res = await apiFetch('/api/vods/' + encodeURIComponent(id) + '/convert', vodConversionRequest(preset));
    if(!res.ok){
      const text = await res.text();
      throw new Error(text.trim() || res.statusText);
    }
    setMessage('Conversion started for VOD ' + id, false);
    currentVODs = currentVODs.map((vod)=>{
      if(vodID(vod) !== id) return vod;
      return {
        ...vod,
        downloaded: true,
        downloadActive: true,
        downloadOperation: 'convert',
        downloadPreset: preset,
        originalSize: vodDownloadSize(vod),
        downloadSize: 0,
      };
    });
    applyVODFilter();
    scheduleProgressPolling();
  }catch(err){
    setMessage('Conversion failed: ' + err.message, true);
    button.disabled = false;
    select.disabled = false;
  }
}

async function deleteDownloadedVOD(id, button){
  if(!id) return;
  button.disabled = true;
  setMessage('Deleting downloaded VOD file...', false);
  try{
    const res = await apiFetch('/api/vods/' + encodeURIComponent(id) + '/download', { method: 'DELETE' });
    if(!res.ok){
      const text = await res.text();
      throw new Error(text.trim() || res.statusText);
    }
    setMessage('Downloaded file deleted for VOD ' + id, false);
    await loadVODs();
  }catch(err){
    setMessage('Delete file failed: ' + err.message, true);
    button.disabled = false;
  }
}

form.addEventListener('submit', addVOD);
if(downloadPresetEl) downloadPresetEl.addEventListener('change', updateDownloadPresetCommand);
filterEl.addEventListener('change', ()=>{
  currentPage = 1;
  applyVODFilter();
});
channelFilterEl.addEventListener('change', ()=>{
  currentPage = 1;
  applyVODFilter();
});
previousPageEl.addEventListener('click', ()=>{
  if(currentPage <= 1) return;
  currentPage -= 1;
  applyVODFilter();
});
nextPageEl.addEventListener('click', ()=>{
  currentPage += 1;
  applyVODFilter();
});
loadVODs();
loadVODPresetCommands();
updateDownloadPresetCommand();
