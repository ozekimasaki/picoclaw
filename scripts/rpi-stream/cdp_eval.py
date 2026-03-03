#!/usr/bin/env python3
"""Execute JS in Chromium via CDP (Chrome DevTools Protocol)."""
import json, subprocess, asyncio, sys

try:
    import websockets
except ImportError:
    subprocess.check_call([sys.executable, '-m', 'pip', 'install', 'websockets', '-q', '--break-system-packages'])
    import websockets

tabs = json.loads(subprocess.check_output(['curl', '-s', 'http://localhost:9222/json/list']))
ws_url = tabs[0]['webSocketDebuggerUrl']
if len(sys.argv) > 2 and sys.argv[1] == '-f':
    with open(sys.argv[2]) as f:
        js = f.read()
else:
    js = sys.argv[1] if len(sys.argv) > 1 else 'document.title'
msg = json.dumps({'id': 1, 'method': 'Runtime.evaluate', 'params': {'expression': js, 'returnByValue': True}})

async def run():
    async with websockets.connect(ws_url) as ws:
        await ws.send(msg)
        resp = json.loads(await ws.recv())
        result = resp.get('result', {}).get('result', {})
        if 'value' in result:
            print(result['value'])
        elif 'description' in result:
            print('ERROR:', result['description'])
        else:
            print(json.dumps(resp, indent=2))

asyncio.run(run())
