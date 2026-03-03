#!/bin/bash
# RPi streaming control script
# Usage: stream.sh start|stop|status|screenshot|setup

set -euo pipefail

SERVICES_STREAM="xvfb chromium-kiosk youtube-stream"
SERVICES_ALL="picoclaw aituber-kit xvfb chromium-kiosk youtube-stream"

case "${1:-help}" in
  start)
    echo "Starting streaming services..."
    sudo systemctl start $SERVICES_STREAM
    echo "Done. Use '$0 status' to check."
    ;;
  stop)
    echo "Stopping streaming services..."
    sudo systemctl stop youtube-stream chromium-kiosk xvfb || true
    echo "Done."
    ;;
  status)
    printf "%-22s %s\n" "SERVICE" "STATUS"
    printf "%-22s %s\n" "-------" "------"
    for svc in $SERVICES_ALL; do
      printf "%-22s %s\n" "$svc" "$(systemctl is-active $svc 2>/dev/null || echo 'unknown')"
    done
    echo
    free -h | head -2
    ;;
  screenshot)
    DISPLAY=:99 import -window root /tmp/screenshot.png
    echo "Saved: /tmp/screenshot.png"
    echo "SCP: scp may999@$(hostname -I | awk '{print $1}'):/tmp/screenshot.png ."
    ;;
  setup)
    echo "=== RPi Streaming Setup ==="

    echo "[1/6] Installing packages..."
    sudo apt update && sudo apt install -y \
      xvfb chromium pulseaudio pulseaudio-utils \
      ffmpeg fonts-noto-cjk imagemagick

    echo "[2/6] Setting up PipeWire virtual sink..."
    mkdir -p ~/.config/pipewire/pipewire.conf.d
    cp "$(dirname "$0")/virtual-sink.conf" ~/.config/pipewire/pipewire.conf.d/virtual-sink.conf
    # Restart PipeWire to pick up the new config
    systemctl --user restart pipewire.service 2>/dev/null || true
    sleep 2
    pactl list sinks short | grep -q virtual_speaker \
      || pactl load-module module-null-sink sink_name=virtual_speaker sink_properties=device.description=Virtual_Speaker
    pactl set-default-sink virtual_speaker

    echo "[3/6] Installing systemd services..."
    sudo cp "$(dirname "$0")/xvfb.service" /etc/systemd/system/
    sudo cp "$(dirname "$0")/chromium-kiosk.service" /etc/systemd/system/
    sudo cp "$(dirname "$0")/youtube-stream.service" /etc/systemd/system/
    sudo systemctl daemon-reload

    echo "[4/6] Setting up stream.env..."
    if [ ! -f ~/.config/stream.env ]; then
      install -m 600 "$(dirname "$0")/stream.env" ~/.config/stream.env
      echo "  ⚠ Edit ~/.config/stream.env and set your YOUTUBE_STREAM_KEY"
    else
      echo "  ~/.config/stream.env already exists, skipping."
    fi

    echo "[5/6] Enabling services + user linger..."
    sudo systemctl enable xvfb chromium-kiosk youtube-stream
    sudo loginctl enable-linger may999
    systemctl --user enable pulseaudio.socket pulseaudio.service 2>/dev/null || true

    echo "[6/6] SIGILL workaround check..."
    AITUBER_DIR="/home/may999/aituber-kit"
    if [ -d "$AITUBER_DIR/node_modules" ]; then
      find "$AITUBER_DIR/node_modules" -name "*.linux-arm64-gnu.node" ! -name "*.bak" 2>/dev/null | head -5
      find "$AITUBER_DIR/node_modules/canvas/build" -name "canvas.node" ! -name "*.bak" 2>/dev/null | head -1
      echo "  If any .node files listed above, run:"
      echo "  find $AITUBER_DIR/node_modules -name '*.linux-arm64-gnu.node' -exec mv {} {}.bak \;"
      echo "  find $AITUBER_DIR/node_modules/canvas/build -name 'canvas.node' -exec mv {} {}.bak \;"
    fi

    echo
    echo "=== Setup complete ==="
    echo "Next steps:"
    echo "  1. Edit ~/.config/stream.env (set YOUTUBE_STREAM_KEY)"
    echo "  2. Update ~/aituber-kit/.env.local (see plan)"
    echo "  3. cd ~/aituber-kit && npm run build"
    echo "  4. Apply SIGILL workaround (rename .node files)"
    echo "  5. sudo systemctl restart aituber-kit"
    echo "  6. $0 start"
    ;;
  help|*)
    echo "Usage: $0 {start|stop|status|screenshot|setup}"
    echo
    echo "  start       - Start streaming (xvfb, chromium, ffmpeg)"
    echo "  stop        - Stop streaming (ffmpeg, chromium, xvfb)"
    echo "  status      - Show all service statuses and memory"
    echo "  screenshot  - Capture Xvfb screen to /tmp/screenshot.png"
    echo "  setup       - First-time setup (install packages, services)"
    ;;
esac
