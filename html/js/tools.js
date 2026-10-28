import { parseTwitchChannelInput } from './utils.js';

const form = document.getElementById('viewStreamForm');
const inputEl = document.getElementById('streamChannelInput');
const submitBtn = document.getElementById('viewStreamBtn');
const msgEl = document.getElementById('viewStreamMsg');

function setMessage(message, isError){
  if(!msgEl) return;
  msgEl.textContent = message || '';
  msgEl.className = isError ? 'form-message text-red-400' : 'form-message';
}

async function handleViewStream(event){
  event.preventDefault();
  const channel = parseTwitchChannelInput(inputEl ? inputEl.value : '');
  if(!channel){
    setMessage('Enter a valid Twitch channel name or URL', true);
    return;
  }

  if(submitBtn) submitBtn.disabled = true;
  setMessage('Checking livestream…', false);
  try{
    const playlistURL = '/api/live-proxy/' + encodeURIComponent(channel) + '/index.m3u8';
    const res = await fetch(playlistURL, { method: 'HEAD', cache: 'no-store' });
    if(!res.ok){
      if(res.status === 404){
        throw new Error('Stream is offline or unavailable');
      }
      throw new Error(res.statusText || 'Could not load livestream');
    }
    window.location.assign('/watch?view=' + encodeURIComponent(channel));
  }catch(err){
    setMessage(err && err.message ? err.message : 'Failed to load livestream', true);
    if(submitBtn) submitBtn.disabled = false;
  }
}

if(form) form.addEventListener('submit', handleViewStream);
