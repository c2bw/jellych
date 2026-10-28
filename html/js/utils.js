const CHANNEL_NAME_PATTERN = /^[a-z0-9_]{1,64}$/i;
const RESERVED_TWITCH_PATHS = new Set(['clip', 'v', 'video', 'videos']);

export function channelNameOf(channel){
  if(typeof channel === 'string') return channel;
  if(channel && typeof channel === 'object') return channel.name || channel.Name || '';
  return '';
}

export function parseTwitchChannelInput(raw){
  if(typeof raw !== 'string') return '';
  const trimmed = raw.trim();
  if(!trimmed) return '';

  const looksLikeURL = /^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed) ||
    /^(?:[\w-]+\.)?twitch\.tv(?:[/?#]|$)/i.test(trimmed);

  if(looksLikeURL){
    const withScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed) ? trimmed : 'https://' + trimmed;
    let parsed;
    try{
      parsed = new URL(withScheme);
    }catch{
      return '';
    }
    if(parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return '';
    const host = parsed.hostname.toLowerCase();
    if(host === 'player.twitch.tv'){
      const ch = (parsed.searchParams.get('channel') || '').trim().replace(/^[@#]/, '').toLowerCase();
      return CHANNEL_NAME_PATTERN.test(ch) ? ch : '';
    }
    if(host === 'clips.twitch.tv') return '';
    if(host !== 'twitch.tv' && !host.endsWith('.twitch.tv')) return '';

    const segments = parsed.pathname.split('/').filter(Boolean);
    if(segments.length !== 1) return '';
    const candidate = decodeURIComponent(segments[0]).trim().replace(/^[@#]/, '').toLowerCase();
    if(RESERVED_TWITCH_PATHS.has(candidate)) return '';
    return CHANNEL_NAME_PATTERN.test(candidate) ? candidate : '';
  }

  const candidate = trimmed.replace(/^[@#]/, '').toLowerCase();
  if(RESERVED_TWITCH_PATHS.has(candidate)) return '';
  return CHANNEL_NAME_PATTERN.test(candidate) ? candidate : '';
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

