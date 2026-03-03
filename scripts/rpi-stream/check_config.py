#!/usr/bin/env python3
import json
with open('/home/may999/.picoclaw/config.json') as f:
    c = json.load(f)
yt = c.get('channels', {}).get('youtube', {})
print('video_id:', yt.get('video_id', '(none)'))
print('channel_id:', yt.get('channel_id', '(none)'))
print('live_chat_id:', yt.get('live_chat_id', '(none)'))
print('chat_source:', yt.get('chat_source', '(default)'))
print('poll_interval:', yt.get('poll_interval_seconds', '(default)'))
