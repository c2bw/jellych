const secretStorageKey = 'jellych_api_secret';
let secretPrompt = null;

export function getControlSecret(){
  try{
    return sessionStorage.getItem(secretStorageKey) || '';
  }catch(_){
    return '';
  }
}

function setControlSecret(secret){
  try{
    sessionStorage.setItem(secretStorageKey, secret);
  }catch(_){
    // Requests can still use the entered secret when storage is unavailable.
  }
}

function requestControlSecret(){
  if(!secretPrompt){
    secretPrompt = Promise.resolve().then(()=>{
      const secret = window.prompt('Jellych control API secret');
      return secret === null ? '' : secret.trim();
    }).finally(()=>{
      secretPrompt = null;
    });
  }
  return secretPrompt;
}

function withControlSecret(options, secret){
  const requestOptions = {...options};
  const headers = new Headers(options && options.headers ? options.headers : undefined);
  if(secret) headers.set('X-Jellych-API-Secret', secret);
  requestOptions.headers = headers;
  return requestOptions;
}

function isControlAuthChallenge(response){
  if(response.status !== 401) return false;
  return (response.headers.get('WWW-Authenticate') || '').includes('jellych-control');
}

// apiFetch adds the session-scoped control secret when available. If the
// server requires authentication, it asks once and retries the request.
export async function apiFetch(input, options = {}){
  let secret = getControlSecret();
  let response = await fetch(input, withControlSecret(options, secret));
  if(!isControlAuthChallenge(response)) return response;

  secret = await requestControlSecret();
  if(!secret) return response;
  setControlSecret(secret);
  return fetch(input, withControlSecret(options, secret));
}
