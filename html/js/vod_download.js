export function vodDownloadRequest(preset = 'original'){
  return {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({preset}),
  };
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
