const result = document.getElementById('chatOAuthResult');
const message = document.getElementById('chatOAuthMessage');

if(result){
  const origin = result.dataset.origin || window.location.origin;
  const error = result.dataset.error || '';
  const payload = error
    ? { type: 'jellych-twitch-chat-oauth', error }
    : {
        type: 'jellych-twitch-chat-oauth',
        accessToken: result.dataset.accessToken || '',
        login: result.dataset.login || '',
        expiresIn: Number(result.dataset.expiresIn) || 0,
      };

  delete result.dataset.accessToken;
  if(window.opener && !window.opener.closed){
    window.opener.postMessage(payload, origin);
    message.textContent = error ? error : 'Login complete. You can close this window.';
    window.setTimeout(()=>window.close(), 50);
  } else {
    message.textContent = error || 'Return to Jellych and start the login again.';
  }
}
