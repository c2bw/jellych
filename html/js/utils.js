export function channelNameOf(channel){
  if(typeof channel === 'string') return channel;
  if(channel && typeof channel === 'object') return channel.name || channel.Name || '';
  return '';
}

export function formatBandwidth(value){
  if(!Number.isFinite(value) || value <= 0) return '—';
  const units = ['bps', 'kbps', 'Mbps', 'Gbps'];
  let size = value;
  let unit = 0;
  while(size >= 1000 && unit < units.length - 1){ size /= 1000; unit += 1; }
  return size.toFixed(size >= 10 ? 0 : 1) + ' ' + units[unit];
}

export function formatTime(value){
  if(!Number.isFinite(value) || value < 0) return '—';
  if(value < 60) return value.toFixed(1) + 's';
  const mins = Math.floor(value / 60);
  const secs = Math.floor(value % 60).toString().padStart(2, '0');
  return mins + ':' + secs;
}

