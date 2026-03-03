// Dismiss Chromium translate bar by clicking the X button
var bar = document.querySelector('[class*="translate"]');
var dismissed = false;

// Try clicking any close/dismiss button in the translate infobar
var allButtons = document.querySelectorAll('button');
for (var i = 0; i < allButtons.length; i++) {
  var b = allButtons[i];
  var text = b.textContent || '';
  var ariaLabel = b.getAttribute('aria-label') || '';
  if (text === '\u00d7' || text === 'x' || text === 'X' || 
      ariaLabel.toLowerCase().includes('close') ||
      ariaLabel.toLowerCase().includes('dismiss')) {
    b.click();
    dismissed = true;
  }
}

// Also try to find and remove the translate bar element directly
var frames = document.querySelectorAll('iframe');
for (var j = 0; j < frames.length; j++) {
  if (frames[j].className && frames[j].className.indexOf('translate') !== -1) {
    frames[j].remove();
    dismissed = true;
  }
}

dismissed ? 'dismissed' : 'no translate bar found';
