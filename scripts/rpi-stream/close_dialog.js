var bs = document.querySelectorAll('button');
for (var b of bs) {
  if (b.textContent.includes('\u9589\u3058\u308b')) {
    b.click();
    'closed';
    break;
  }
}
