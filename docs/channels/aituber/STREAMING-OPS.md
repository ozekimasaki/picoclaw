# RPi 配信オペレーション手順

> ホスト: `pi-sub01` (192.168.0.68) / ユーザー: `may999`

## 前提条件

- Windows PC で VOICEVOX Engine が起動していること
  ```
  "C:\Program Files\VOICEVOX\vv-engine\run.exe" --use_gpu --host 0.0.0.0 --port 50021
  ```
- YouTube Studio で配信枠が作成済みであること
- `~/.config/stream.env` に `YOUTUBE_STREAM_KEY` が設定済みであること

## サービス構成

```
youtube-stream  ← 配信の親。stop すると全部止まる
  ├─ chromium-kiosk  (PartOf=youtube-stream)
  │    └─ xvfb       (PartOf=chromium-kiosk)
  ├─ aituber-kit     (PartOf=youtube-stream)
  └─ picoclaw        (PartOf=youtube-stream)
```

## 配信開始

```bash
# 1. 全サービス一括起動
sudo systemctl start youtube-stream

# 2. 状態確認
bash ~/scripts/rpi-stream/stream.sh status
```

> `youtube-stream` を start すると `picoclaw`, `aituber-kit`, `xvfb`, `chromium-kiosk` も連動して起動します。
> ffmpeg は Chromium 起動後 15 秒待ってから RTMP 送出を開始します。

## 配信停止

```bash
# 全サービス一括停止
sudo systemctl stop youtube-stream
```

> `youtube-stream` を stop すると 5 サービスすべてが連動停止します。

## 状態確認

```bash
bash ~/scripts/rpi-stream/stream.sh status
```

出力例:
```
SERVICE                STATUS
-------                ------
picoclaw               active
aituber-kit            active
xvfb                   active
chromium-kiosk         active
youtube-stream         active

               total        used        free      shared  buff/cache   available
Mem:           3.7Gi       1.0Gi       826Mi       148Mi       2.1Gi       2.7Gi
```

## スクリーンショット確認

```bash
# RPi 上で取得
bash ~/scripts/rpi-stream/stream.sh screenshot

# Windows から取得
scp may999@192.168.0.68:/tmp/screenshot.png .
```

## CPU 負荷確認

```bash
top -bn1 | head -5
top -bn1 | grep ffmpeg
```

| 設定 | ffmpeg CPU | 備考 |
|---|---|---|
| 720p/30fps/4500k | ~46% | **採用** |
| 1080p/20fps/6000k | ~125% | RPi4 では過負荷 |

## トラブルシューティング

### ffmpeg が failed になる

```bash
sudo systemctl reset-failed youtube-stream
sudo systemctl start youtube-stream
```

### 翻訳バーが表示される

```bash
# Chromium プロファイルをリセット
rm -rf ~/.chromium-kiosk
sudo systemctl restart youtube-stream
```

### コメントに反応しない

```bash
# PicoClaw ログ確認
journalctl -u picoclaw --since '5 min ago' --no-pager -n 20

# video_id をクリア（RSS 自動検出に戻す）
python3 ~/scripts/rpi-stream/clear_video_id.py
sudo systemctl restart picoclaw
```

### VOICEVOX に接続できない

- Windows PC で Engine が `--host 0.0.0.0` で起動しているか確認
- RPi から疎通確認:
  ```bash
  curl -s http://192.168.0.194:50021/version
  ```

## 配信パラメータ

設定ファイル: `~/.config/stream.env`

```
YOUTUBE_STREAM_KEY=xxxx-xxxx-xxxx-xxxx
RESOLUTION=1280x720
FPS=30
VIDEO_BITRATE=4500k
AUDIO_BITRATE=128k
GOP=60
```

変更後は `sudo systemctl restart youtube-stream` で反映。
