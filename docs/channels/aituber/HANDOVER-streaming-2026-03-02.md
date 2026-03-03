# 引継書: RPi 4 YouTube 配信パイプライン

> 作成日: 2026-03-02
> 対象: pi-sub01 (192.168.0.68) / ユーザー: may999

---

## 1. 実装概要

Raspberry Pi 4 (4GB) 単体で PicoClaw + AITuber Kit (PNGTuber) を YouTube RTMP 配信する仕組みを構築した。OBS 不要・追加ハードウェア不要。

### 全体構成

```
┌─ Windows PC ──────────┐       ┌─ RPi 4 (4GB) ─────────────────────────┐
│ VOICEVOX Engine       │       │                                        │
│  --host 0.0.0.0       │       │ PicoClaw (Go)                   ~50MB │
│  :50021               │◄─LAN─►│  ├─ YouTube Chat (InnerTube)          │
│                       │       │  ├─ AITuber WS (localhost:8000)       │
└───────────────────────┘       │  └─ sendWorker ← tts_complete 待ち    │
                                │                                        │
                                │ AITuber Kit (Next.js :3000)           │
                                │  ├─ PNGTuber (Canvas 2D, no WebGL)    │
                                │  ├─ /api/tts-voicevox → LAN VOICEVOX  │
                                │  └─ tts_complete → WS                 │
                                │                                        │
                                │ Xvfb :99 (1280x720x24)               │
                                │ Chromium (kiosk, --disable-gpu)       │
                                │  └─ AudioContext → PipeWire            │
                                │ PipeWire (null-sink: virtual_speaker) │
                                │ ffmpeg (x11grab + pulse → RTMP)       │
                                │                                        │
                                │ 実測メモリ: ~1.0GB / 3.7GB            │
                                └────────────────────────────────────────┘
```

### データフロー

```
YouTube Chat ─(InnerTube)→ PicoClaw preFilter → accumulate
  → bus → LLM([emotion] text) → bus → YouTube.Send()
  → forward_channel:"aituber" → AITuber sendQueue → sendWorker
  → WS broadcast → Chromium (AITuber Kit)
  → handleReceiveTextFromWsFn() → speakCharacter()
  → /api/tts-voicevox (Next.js API route, server-side)
  → VOICEVOX Engine (LAN) → audio WAV
  → PNGTuberHandler.speak() → PNGTuberEngine.playAudioFromBuffer()
  → AudioContext → [PipeWire null-sink] → [ffmpeg] → RTMP
  → SpeakQueue.onSpeakCompletion → {"type":"tts_complete"} → WS → PicoClaw
```

---

## 2. 実装したコード変更

### 2.1 tts_complete コールバック (CRITICAL バグ修正)

AITuber Kit が TTS 再生完了を PicoClaw に通知する仕組みが欠落していた。これがないと PicoClaw の sendWorker が次のメッセージを送信できない。

**`aituber-kit/src/utils/WebSocketManager.ts`**
- `send(data: string): boolean` メソッドを追加 (L128-134)
- WebSocket が OPEN 状態の時のみデータを送信

**`aituber-kit/src/components/useExternalLinkage.tsx`**
- `SpeakQueue.onSpeakCompletion` コールバックで `{"type":"tts_complete"}` を WebSocket 送信 (L86-90)
- クリーンアップ時に `removeSpeakCompletionCallback` を呼ぶ (L111)
- WebSocket URL をハードコード `ws://localhost:8000/ws` (L81) — RPi 上では localhost で完結するため

### 2.2 配信パイプライン (scripts/rpi-stream/)

#### systemd サービスファイル

| ファイル | 説明 |
|---|---|
| `xvfb.service` | Xvfb 仮想フレームバッファ (:99, 1280x720x24) |
| `chromium-kiosk.service` | Chromium キオスクモード + PipeWire virtual sink 作成 |
| `youtube-stream.service` | ffmpeg x11grab + pulse → YouTube RTMP |

#### サービス連動設定 (PartOf)

```
youtube-stream.service        ← 親。start/stop で全連動
  ├─ chromium-kiosk.service   (PartOf=youtube-stream, WantedBy=youtube-stream)
  │    └─ xvfb.service        (PartOf=chromium-kiosk, WantedBy=chromium-kiosk)
  ├─ aituber-kit.service      (PartOf=youtube-stream, WantedBy=youtube-stream)
  └─ picoclaw.service         (PartOf=youtube-stream, WantedBy=youtube-stream)
```

`sudo systemctl stop youtube-stream` で 5 サービスすべてが連動停止する。

#### ユーティリティスクリプト

| ファイル | 説明 |
|---|---|
| `stream.sh` | start/stop/status/screenshot/setup の統合スクリプト |
| `stream.env` | 配信パラメータ (テンプレート。実体は `~/.config/stream.env`) |
| `virtual-sink.conf` | PipeWire null-sink 設定 (`~/.config/pipewire/pipewire.conf.d/`) |
| `cdp_eval.py` | CDP 経由で Chromium に JS を実行するツール |
| `close_dialog.js` | AITuber Kit 初回ダイアログを閉じる JS |
| `dismiss_translate.js` | Chromium 翻訳バーを閉じる JS |
| `fix_translate_prefs.py` | Chromium Preferences で翻訳を無効化 |
| `clear_video_id.py` | config.json の video_id をクリア (RSS 自動検出に戻す) |
| `check_config.py` | PicoClaw config の YouTube 設定を確認 |
| `check_forward.py` | forward_channel / AITuber チャネル設定を確認 |

---

## 3. 配信パラメータ (最終値)

```
RESOLUTION=1280x720
FPS=30
VIDEO_BITRATE=4500k
AUDIO_BITRATE=128k
GOP=60
```

### ffmpeg コマンド (youtube-stream.service)

```bash
ffmpeg -nostdin -loglevel warning \
  -f x11grab -video_size 1280x720 \
  -framerate 30 -thread_queue_size 512 -i :99 \
  -f pulse -thread_queue_size 512 -i virtual_speaker.monitor \
  -vf format=yuv420p \
  -c:v libx264 -preset ultrafast -tune zerolatency \
  -b:v 4500k -maxrate 4500k -bufsize 9000k \
  -g 60 -keyint_min 60 \
  -threads 3 \
  -c:a aac -b:a 128k -ar 44100 \
  -f flv "rtmp://a.rtmp.youtube.com/live2/${YOUTUBE_STREAM_KEY}"
```

### CPU 負荷実測値 (RPi 4)

| 設定 | ffmpeg CPU | メモリ | 判定 |
|---|---|---|---|
| 720p/15fps/2500k | ~30% | 1.2GB | 軽いが画質低い |
| **720p/30fps/4500k** | **~46%** | **1.0GB** | **採用** |
| 1080p/20fps/6000k | ~125% | 1.1GB | NG (CPU 過負荷) |
| 1080p/30fps/6000k | ~125% | 1.1GB | NG (CPU 過負荷) |

> RPi 4 の libx264 では 1080p はどの FPS でも過負荷。720p が限界。

### Chromium 起動フラグ

```
--remote-debugging-port=9222
--kiosk
--no-first-run
--disable-infobars
--disable-session-crashed-bubble
--disable-gpu
--disable-software-rasterizer
--autoplay-policy=no-user-gesture-required
--lang=ja
--disable-features=Translate,TranslateUI
--js-flags=--max-old-space-size=512
--window-size=1280,720
--user-data-dir=/home/may999/.chromium-kiosk
```

---

## 4. 解決した問題と対処法

### 4.1 色反転 (x11grab BGR → RGB)

**症状**: 配信映像の色が青みがかって異常  
**原因**: x11grab が BGR で出力するが、FLV/YouTube は YUV420P を期待  
**対処**: ffmpeg に `-vf format=yuv420p` を追加

### 4.2 Chromium 翻訳バー

**症状**: Chromium に翻訳ボタンが表示される  
**対処** (3 段階):
1. `--lang=ja` を追加
2. `--disable-features=Translate,TranslateUI` に変更 (`TranslateUI` だけでは不十分)
3. Chromium プロファイルリセット (`rm -rf ~/.chromium-kiosk`)

### 4.3 PicoClaw がコメントに反応しない

**症状**: YouTube コメントを打っても AITuber が反応しない  
**原因**: config.json に古い `video_id` がハードコードされていた  
**対処**: `clear_video_id.py` で video_id をクリア → RSS 自動検出に切り替え

### 4.4 PipeWire 対応 (Debian Trixie)

**症状**: PulseAudio のコマンドが動かない  
**原因**: Debian Trixie は PulseAudio → PipeWire に移行済み  
**対処**:
- `virtual-sink.conf` を PipeWire 形式で作成
- `chromium-kiosk.service` の ExecStartPre で `pactl` フォールバック追加
- `stream.sh setup` で PipeWire conf ディレクトリにコピー

### 4.5 SIGILL (Cortex-A72 ARMv8.0)

**症状**: AITuber Kit が起動時に SIGILL でクラッシュ  
**原因**: ネイティブ .node モジュールが ARMv8.2+ 命令を使用  
**対処**: `.node` → `.node.bak` にリネームして JS fallback に切り替え  
**対象モジュール**:
- `@napi-rs/canvas-linux-arm64-gnu/skia.linux-arm64-gnu.node`
- `@unrs/resolver-binding-linux-arm64-gnu/resolver.linux-arm64-gnu.node`
- `canvas/build/Release/canvas.node`

> `npm install` 後は毎回リネームが必要。

### 4.6 Node.js バージョン

- Node v22 → v20 にダウングレード (v22 も SIGILL 関連で問題あり)
- nodesource repo 経由でインストール

### 4.7 systemd の注意点

- `%U` は system unit では UID 0 に展開される → `XDG_RUNTIME_DIR=/run/user/1000` を明示
- `next start` には `--hostname 0.0.0.0` が必須 (`-H` では不十分)
- `ffmpeg` には `-nostdin` が必須 (systemd 下で stdin が /dev/null でない場合がある)
- Debian Trixie では `chromium-browser` ではなく `chromium`

---

## 5. RPi 上のファイル配置

```
/home/may999/
├── picoclaw                      # PicoClaw バイナリ (linux/arm64)
├── .picoclaw/
│   ├── config.json               # PicoClaw 設定
│   └── workspace/
│       └── SOUL.md               # キャラクター設定 (エレナ)
├── aituber-kit/                  # AITuber Kit (Next.js)
│   ├── .env.local                # 環境変数
│   └── node_modules/             # SIGILL .node は .bak 済み
├── .config/
│   ├── stream.env                # 配信パラメータ (600 パーミッション)
│   └── pipewire/
│       └── pipewire.conf.d/
│           └── virtual-sink.conf # PipeWire null-sink
├── .chromium-kiosk/              # Chromium ユーザーデータ
└── scripts/
    └── rpi-stream/               # 配信関連スクリプト・サービス一式
```

```
/etc/systemd/system/
├── picoclaw.service
├── aituber-kit.service
├── xvfb.service
├── chromium-kiosk.service
└── youtube-stream.service
```

---

## 6. ポート一覧

| ポート | サービス | 用途 |
|---|---|---|
| 3000 | AITuber Kit | Web UI (Next.js) |
| 8000 | PicoClaw | AITuber WebSocket |
| 9222 | Chromium | CDP リモートデバッグ |
| 18790 | PicoClaw | Gateway ヘルスチェック |
| 50021 | VOICEVOX (Windows) | TTS エンジン (LAN) |

---

## 7. 運用コマンド

```bash
# 配信開始 (5 サービス一括)
sudo systemctl start youtube-stream

# 配信停止 (全連動)
sudo systemctl stop youtube-stream

# 状態確認
bash ~/scripts/rpi-stream/stream.sh status

# スクリーンショット
bash ~/scripts/rpi-stream/stream.sh screenshot

# CPU 負荷
top -bn1 | grep ffmpeg

# PicoClaw ログ
journalctl -u picoclaw -f

# AITuber Kit ログ
journalctl -u aituber-kit -f

# ffmpeg ログ
journalctl -u youtube-stream -f

# CDP で JS 実行
python3 ~/scripts/rpi-stream/cdp_eval.py -f ~/scripts/rpi-stream/close_dialog.js

# ヘルスチェック
curl http://localhost:18790/health
curl http://localhost:8000/health/aituber
```

---

## 8. 関連ドキュメント

| ファイル | 説明 |
|---|---|
| `docs/channels/aituber/DEPLOY-RPi4.md` | 初回デプロイ手順 (Step 1〜8) |
| `docs/channels/aituber/STREAMING-OPS.md` | 日常の配信オペレーション手順 |
| `docs/channels/aituber/SOUL-template.md` | SOUL.md テンプレート |

---

## 9. 既知の制限事項

1. **1080p 非対応**: RPi 4 の libx264 では 720p が実用上限
2. **ハードウェアエンコーダなし**: RPi 4 の h264_v4l2m2m は使えない (品質問題)
3. **VOICEVOX は Windows 必須**: RPi 上では動作しない (x86 専用)
4. **npm install 後は SIGILL 対策必須**: .node ファイルの手動リネーム
5. **YouTube 自動開始**: YouTube Studio で「ストリーム受信時に自動的にライブ配信を開始する」をオンにしないと RTMP 送出だけでは配信が始まらない
6. **ストリームキー管理**: `~/.config/stream.env` に平文保存 (パーミッション 600)

---

## 10. 今後の改善候補

- [ ] stream.sh に `youtube-stream` 一本化を反映 (現在は個別サービス名を列挙)
- [ ] VOICEVOX の Docker 化 or ARM 対応待ち
- [ ] RPi 5 での 1080p 再検証
- [ ] InnerTube 実装の RPi デプロイ (現在未デプロイ)
- [ ] 自動配信スケジューラ (cron + systemctl)
