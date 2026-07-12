export function vodDownloadRequest(preset = 'original'){
  return {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({preset}),
  };
}

export function vodConversionRequest(preset){
  return {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({preset}),
  };
}

export function vodPresetCommand(preset = 'original'){
  const commands = {
    original: '-c copy',
    h264: '-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 128k -c:s copy',
    hevc: '-c:v libx265 -preset medium -crf 25 -c:a aac -b:a 128k -c:s copy',
    vp9: '-c:v libvpx-vp9 -crf 32 -b:v 0 -deadline good -cpu-used 2 -c:a libopus -b:a 128k -c:s copy',
  };
  return commands[preset] || commands.original;
}

export function formatVODRemainingTime(seconds){
  const value = Math.max(0, Number(seconds) || 0);
  const hours = Math.floor(value / 3600);
  const minutes = Math.ceil((value % 3600) / 60);
  if(hours > 0) return '~' + hours + 'h ' + minutes + 'm remaining';
  return '~' + Math.max(minutes, 1) + 'm remaining';
}

export function formatVODMediaInfo(codec, height, totalBitrate = 0){
  const normalizedCodec = String(codec || '').toLowerCase();
  const codecLabel = ({h264: 'H.264', hevc: 'HEVC', vp9: 'VP9', av1: 'AV1'})[normalizedCodec] || normalizedCodec.toUpperCase();
  const numericHeight = Number(height);
  const qualityLabel = Number.isFinite(numericHeight) && numericHeight > 0 ? Math.round(numericHeight) + 'p' : '';
  const numericBitrate = Number(totalBitrate);
  const bitrateLabel = Number.isFinite(numericBitrate) && numericBitrate > 0 ? (numericBitrate / 1_000_000).toFixed(2) + ' Mbps' : '';
  return [codecLabel, qualityLabel, bitrateLabel].filter(Boolean).join(' · ');
}

export function appendVODDownloadPresetBadge(documentRef, container, {downloaded, active, preset}){
  if((!downloaded && !active) || !preset) return false;

  const labels = {original: 'Original', h264: 'H.264', hevc: 'HEVC', vp9: 'VP9'};
  const separator = documentRef.createElement('span');
  separator.className = 'hidden sm:inline';
  separator.textContent = '-';
  const badge = documentRef.createElement('span');
  badge.className = 'shrink-0 rounded border border-[#4aa3ff]/30 bg-[#4aa3ff]/10 px-2 py-0.5 text-xs font-semibold text-[#8fc7ff]';
  badge.textContent = labels[preset] || preset;
  container.appendChild(separator);
  container.appendChild(badge);
  return true;
}

export function appendVODConversionControl(documentRef, container, {title, onConvert}){
  const separator = documentRef.createElement('span');
  separator.className = 'hidden sm:inline';
  separator.textContent = '-';
  const select = documentRef.createElement('select');
  select.className = 'shrink-0 rounded border border-[#4aa3ff]/30 bg-[#4aa3ff]/10 px-2 py-0.5 text-xs font-semibold text-[#8fc7ff] outline-none';
  select.setAttribute('aria-label', 'Conversion preset for ' + title);
  for(const [value, label] of [['original', 'Original'], ['h264', 'H.264'], ['hevc', 'HEVC'], ['vp9', 'VP9']]){
    const option = documentRef.createElement('option');
    option.value = value;
    option.textContent = label;
    select.appendChild(option);
  }
  const button = documentRef.createElement('button');
  button.type = 'button';
  button.textContent = 'Convert';
  button.className = 'button hidden px-2 py-0.5 text-xs';
  select.addEventListener('change', ()=>button.classList.toggle('hidden', select.value === 'original'));
  button.addEventListener('click', ()=>onConvert(select.value, button, select));
  container.appendChild(separator);
  container.appendChild(select);
  container.appendChild(button);
  return {select, button};
}
