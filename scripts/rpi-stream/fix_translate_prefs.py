#!/usr/bin/env python3
"""Disable Chromium translation bar via Preferences file."""
import json, os

pref_path = '/home/may999/.chromium-kiosk/Default/Preferences'
os.makedirs(os.path.dirname(pref_path), exist_ok=True)

prefs = {}
if os.path.exists(pref_path):
    with open(pref_path) as f:
        try:
            prefs = json.load(f)
        except json.JSONDecodeError:
            prefs = {}

prefs['translate'] = {'enabled': False}
prefs['translate_blocked_languages'] = ['ja', 'en']
prefs.setdefault('intl', {})['accept_languages'] = 'ja,en-US,en'

with open(pref_path, 'w') as f:
    json.dump(prefs, f, indent=2)

print('Chromium translate disabled in Preferences')
