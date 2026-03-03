#!/usr/bin/env python3
import json
with open('/home/may999/.picoclaw/config.json') as f:
    c = json.load(f)
yt = c['channels']['youtube']
print('forward_channel:', yt.get('forward_channel', 'MISSING'))
print('forward_chat_id:', yt.get('forward_chat_id', 'MISSING'))
at = c['channels'].get('aituber', {})
print('aituber enabled:', at.get('enabled', False))
print('aituber ws_host:', at.get('ws_host', 'MISSING'))
print('aituber ws_port:', at.get('ws_port', 'MISSING'))
