(function() {
  var r = {};
  // Check SpeakQueue callback count
  try {
    var sq = require('@/features/messages/speakQueue');
  } catch(e) {}
  // Check WebSocket readyState via console interception
  var allWs = [];
  var origWs = window._origWs || [];
  r.wsCount = origWs.length;
  // Try to find open WebSockets by checking performance entries
  var wsEntries = performance.getEntriesByType('resource').filter(function(e) {
    return e.name.indexOf('ws://') === 0 || e.name.indexOf('wss://') === 0;
  });
  r.wsEntries = wsEntries.map(function(e) { return e.name; });
  return JSON.stringify(r);
})()
