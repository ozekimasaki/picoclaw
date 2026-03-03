#!/usr/bin/env python3
"""Clear video_id from config so PicoClaw auto-detects via RSS."""
import json

path = '/home/may999/.picoclaw/config.json'
with open(path) as f:
    c = json.load(f)

yt = c.get('channels', {}).get('youtube', {})
old = yt.get('video_id', '')
yt['video_id'] = ''
print(f'Cleared video_id (was: {old})')

with open(path, 'w') as f:
    json.dump(c, f, indent=2, ensure_ascii=False)
