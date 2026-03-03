(function() {
  try {
    var found = false;
    var frames = document.querySelectorAll('iframe');
    // Next.js bundles modules in webpack chunks - access via __next_f or window
    // The SpeakQueue callbacks are static, so we need to find them
    // Easiest: directly send tts_complete via a new WebSocket to verify the PicoClaw side
    var ws = new WebSocket('ws://localhost:8000/ws');
    ws.onopen = function() {
      ws.send(JSON.stringify({type: 'tts_complete'}));
      console.log('tts_complete sent via test WebSocket');
      setTimeout(function() { ws.close(); }, 500);
    };
    ws.onerror = function(e) { console.error('WS error', e); };
    return 'tts_complete test: WebSocket opened, will send on connect';
  } catch(e) {
    return 'error: ' + e.message;
  }
})()
