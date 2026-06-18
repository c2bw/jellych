const form = document.getElementById('vodForm');
const idInput = document.getElementById('vodId');
const addButton = document.getElementById('addVodBtn');
const msgEl = document.getElementById('vodMsg');
const listEl = document.getElementById('vodList');
const filterEl = document.getElementById('vodFilter');
let currentVODs = [];
const vodIDLength = 10;

function setMessage(message, isError){
  msgEl.textContent = message || '';
  msgEl.className = isError ? 'min-h-[20px] text-sm text-red-400' : 'min-h-[20px] text-sm text-white/50';
}

async function fetchJSON(url, options){
  const res = await fetch(url, options);
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
  playlist.href = '/vod/' + encodeURIComponent(id) + '/index.m3u8';
  playlist.textContent = 'Playlist';
  playlist.className = 'button no-underline';

  const downloadAction = document.createElement('button');
  downloadAction.type = 'button';
  if(vodDownloaded(vod)){
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
  const separator = document.createElement('span');
  separator.className = 'hidden sm:inline';
  separator.textContent = '-';
  const urlText = document.createElement('span');
  urlText.className = 'min-w-0 flex-1 truncate';
  urlText.textContent = vodURL(vod);
  meta.appendChild(dateText);
  meta.appendChild(separator);
  meta.appendChild(urlText);

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
  row.appendChild(info);
  return row;
}

function renderVODs(vods){
  listEl.innerHTML = '';
  if(!Array.isArray(vods) || vods.length === 0){
    const onlyDownloaded = filterEl && filterEl.value === 'downloaded';
    renderEmpty(onlyDownloaded ? 'No downloaded VODs found.' : 'No VODs saved yet.');
    return;
  }
  const frag = document.createDocumentFragment();
  sortVODsByDate(vods).forEach((vod)=>frag.appendChild(renderVOD(vod)));
  listEl.appendChild(frag);
}

function applyVODFilter(){
  const onlyDownloaded = filterEl && filterEl.value === 'downloaded';
  const vods = onlyDownloaded ? currentVODs.filter(vodDownloaded) : currentVODs;
  renderVODs(vods);
}

async function loadVODs(){
  try{
    currentVODs = await fetchJSON('/api/vods');
    applyVODFilter();
  }catch(err){
    listEl.textContent = 'Could not load VODs: ' + err.message;
  }
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
    const res = await fetch('/api/vods/' + encodeURIComponent(id), { method: 'DELETE' });
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
    const res = await fetch('/api/vods/' + encodeURIComponent(id) + '/download', { method: 'POST' });
    if(!res.ok){
      const text = await res.text();
      throw new Error(text.trim() || res.statusText);
    }
    setMessage('Download started for VOD ' + id, false);
  }catch(err){
    setMessage('Download failed: ' + err.message, true);
    button.disabled = false;
  }
}

async function deleteDownloadedVOD(id, button){
  if(!id) return;
  button.disabled = true;
  setMessage('Deleting downloaded VOD file...', false);
  try{
    const res = await fetch('/api/vods/' + encodeURIComponent(id) + '/download', { method: 'DELETE' });
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
filterEl.addEventListener('change', applyVODFilter);
loadVODs();
