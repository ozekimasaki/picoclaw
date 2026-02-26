# Raspberry Pi 4 デプロイガイド

PicoClaw + AITuber Kit を RPi 4 上でヘッドレスサーバーとして動かし、LAN 上の別 PC のブラウザからアクセスする構成です。

## 前提条件

- Raspberry Pi 4 (4GB / 8GB)
- Raspberry Pi OS Lite (64-bit) または Ubuntu Server 24.04 ARM64
- Node.js 18+ (AITuber Kit 用)
- Go 1.22+ (PicoClaw ビルド済みバイナリ使用なら不要)
- LAN 接続済み

## アーキテクチャ

```
┌─ RPi 4 (ヘッドレス) ─────────────────────┐
│                                           │
│  PicoClaw gateway (systemd)               │
│    ├─ YouTube Live Chat ポーリング         │
│    ├─ LLM API 呼び出し (外部)              │
│    └─ WebSocket Server (0.0.0.0:8000)     │
│                                           │
│  AITuber Kit (systemd / pm2)              │
│    └─ Next.js (0.0.0.0:3000)              │
│                                           │
└───────────────┬───────────────────────────┘
                │ LAN
┌───────────────┴───────────────────────────┐
│  別 PC ブラウザ                             │
│  http://raspberrypi.local:3000            │
│    └─ キャラクター描画 + TTS 再生           │
│    └─ ws://raspberrypi.local:8000/ws      │
└───────────────────────────────────────────┘
```

## メモリバジェット（4GB モデル）

| プロセス | 想定メモリ |
|---|---|
| OS + systemd | ~300MB |
| PicoClaw gateway | ~30-50MB |
| Node.js (AITuber Kit) | ~150-250MB |
| **合計** | **~0.5-0.6GB** |

残り 3.4GB+ が空くため、余裕があります。

---

## Step 1: PicoClaw バイナリの準備

### 方法 A: 開発 PC でクロスコンパイル（推奨）

```bash
# 開発 PC (Windows/Mac/Linux) で実行
cd picoclaw
make build-linux-arm64
# → dist/picoclaw-linux-arm64 が生成される
```

### 方法 B: RPi 上で直接ビルド

```bash
# RPi で Go をインストール
sudo apt update && sudo apt install -y golang-go git
git clone https://github.com/ozekimasaki/picoclaw.git
cd picoclaw
make build
```

## Step 2: PicoClaw を RPi に配置

```bash
# 開発 PC から RPi に転送 (方法 A の場合)
scp dist/picoclaw-linux-arm64 pi@raspberrypi.local:/home/pi/picoclaw

# RPi 上で実行権限付与
ssh pi@raspberrypi.local
chmod +x /home/pi/picoclaw

# 初期セットアップ
/home/pi/picoclaw onboard
```

## Step 3: PicoClaw 設定

```bash
mkdir -p ~/.picoclaw
nano ~/.picoclaw/config.json
```

```json
{
  "agents": {
    "defaults": {
      "workspace": "/home/pi/.picoclaw/workspace",
      "restrict_to_workspace": true,
      "model_name": "my-llm",
      "max_tokens": 4096,
      "temperature": 0.7
    }
  },
  "model_list": [
    {
      "model_name": "my-llm",
      "model": "openai/your-model-name",
      "api_key": "sk-your-api-key",
      "api_base": "https://your-api-endpoint/v1"
    }
  ],
  "channels": {
    "youtube": {
      "enabled": true,
      "api_key": "YOUR_YOUTUBE_API_KEY",
      "video_id": "YOUR_VIDEO_ID",
      "poll_interval_seconds": 30,
      "forward_channel": "aituber",
      "forward_chat_id": "default",
      "ng_words": [],
      "min_message_length": 2,
      "max_repeat_ratio": 0.7,
      "block_urls": true,
      "max_comments_per_poll": 2,
      "selection_strategy": "latest",
      "batch_comments": true
    },
    "aituber": {
      "enabled": true,
      "ws_host": "0.0.0.0",
      "ws_port": 8000,
      "ws_path": "/ws",
      "default_emotion": "neutral",
      "max_queue_size": 5
    }
  },
  "gateway": {
    "host": "0.0.0.0",
    "port": 18790
  }
}
```

### RPi 向け推奨値

| 設定 | 推奨値 | 理由 |
|---|---|---|
| `poll_interval_seconds` | `30` | API コール頻度を下げる |
| `max_comments_per_poll` | `2` | LLM 呼び出し回数を抑える |
| `batch_comments` | `true` | 複数コメントを1回の LLM 呼び出しでまとめ処理 |
| `max_queue_size` | `5` | メモリ節約 |
| `max_tokens` | `4096` | 応答を短く保つ |

## Step 4: SOUL.md を設定

```bash
mkdir -p ~/.picoclaw/workspace
nano ~/.picoclaw/workspace/SOUL.md
```

`docs/channels/aituber/SOUL-template.md` の内容をコピーし、キャラクター設定をカスタマイズしてください。重要なのは LLM に `[emotion] テキスト` 形式で出力させる指示です。

## Step 5: AITuber Kit のセットアップ

```bash
# Node.js インストール (未インストールの場合)
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs

# AITuber Kit をクローン (submodule として既にある場合はスキップ)
cd /home/pi
git clone https://github.com/tegnike/aituber-kit.git
cd aituber-kit

# 環境変数設定
cat > .env.local << 'EOF'
NEXT_PUBLIC_EXTERNAL_LINKAGE_MODE=true
NEXT_PUBLIC_WS_URL=ws://localhost:8000/ws
EOF

# 依存関係インストール & ビルド
npm install
npm run build
```

> **注意**: `NEXT_PUBLIC_WS_URL` はブラウザから見た WebSocket URL です。  
> ブラウザは別 PC 上で動くため、`localhost` ではなく RPi の IP またはホスト名を使います。  
> ただし `NEXT_PUBLIC_*` はビルド時に埋め込まれるので、**ブラウザからアクセスする場合は RPi の IP を指定して再ビルド**が必要です。

```bash
# RPi の IP を確認
hostname -I
# 例: 192.168.1.50

# 別 PC からアクセスする場合の .env.local
cat > .env.local << 'EOF'
NEXT_PUBLIC_EXTERNAL_LINKAGE_MODE=true
NEXT_PUBLIC_WS_URL=ws://192.168.1.50:8000/ws
EOF

# 再ビルド
npm run build
```

## Step 6: systemd サービスの作成

### PicoClaw

```bash
sudo tee /etc/systemd/system/picoclaw.service << 'EOF'
[Unit]
Description=PicoClaw AI Assistant Gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi
ExecStart=/home/pi/picoclaw gateway
Restart=always
RestartSec=10
Environment=TZ=Asia/Tokyo

[Install]
WantedBy=multi-user.target
EOF
```

### AITuber Kit

```bash
sudo tee /etc/systemd/system/aituber-kit.service << 'EOF'
[Unit]
Description=AITuber Kit Next.js Server
After=network-online.target picoclaw.service
Wants=network-online.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi/aituber-kit
ExecStart=/usr/bin/npm start
Restart=always
RestartSec=10
Environment=PORT=3000
Environment=HOSTNAME=0.0.0.0
Environment=NODE_ENV=production
Environment=TZ=Asia/Tokyo

[Install]
WantedBy=multi-user.target
EOF
```

### サービスの有効化と起動

```bash
sudo systemctl daemon-reload

# PicoClaw
sudo systemctl enable picoclaw
sudo systemctl start picoclaw

# AITuber Kit
sudo systemctl enable aituber-kit
sudo systemctl start aituber-kit

# 状態確認
sudo systemctl status picoclaw
sudo systemctl status aituber-kit
```

## Step 7: 動作確認

### RPi 上で確認

```bash
# PicoClaw ヘルスチェック
curl http://localhost:18790/health

# AITuber WebSocket ヘルスチェック
curl http://localhost:8000/health/aituber

# ログ確認
journalctl -u picoclaw -f
journalctl -u aituber-kit -f
```

### 別 PC のブラウザで確認

```
http://raspberrypi.local:3000
```

または RPi の IP アドレスで:

```
http://192.168.1.50:3000
```

AITuber Kit の UI が表示され、キャラクターが見えれば成功です。

## トラブルシューティング

| 症状 | 確認コマンド | 対策 |
|---|---|---|
| PicoClaw が起動しない | `journalctl -u picoclaw -n 50` | config.json の構文確認、API キー確認 |
| AITuber Kit が起動しない | `journalctl -u aituber-kit -n 50` | `npm run build` が成功しているか確認 |
| ブラウザから接続できない | `curl http://RPi-IP:3000` | ファイアウォール確認、`HOSTNAME=0.0.0.0` 確認 |
| WebSocket 接続エラー | `curl http://RPi-IP:8000/health/aituber` | `ws_host: "0.0.0.0"` 確認 |
| キャラが喋らない | ブラウザ DevTools Console | AITuber Kit の TTS 設定確認 |
| YouTube コメントが反映されない | `journalctl -u picoclaw -f` | API キー、Video ID、ライブ配信中か確認 |
| 応答に感情がつかない | PicoClaw ログ | SOUL.md に感情タグ出力指示があるか確認 |
| メモリ不足 | `free -h` | `max_queue_size` を下げる、不要サービス停止 |

## ファイアウォール設定（必要な場合）

```bash
# ufw を使用している場合
sudo ufw allow 3000/tcp    # AITuber Kit UI
sudo ufw allow 8000/tcp    # WebSocket
sudo ufw allow 18790/tcp   # PicoClaw health (オプション)
```

## PicoClaw コマンドリファレンス

```bash
# ── 基本コマンド ──────────────────────────

# 初期セットアップ（ワークスペース作成、SOUL.md 生成）
picoclaw onboard

# ワンショット質問
picoclaw agent -m "こんにちは"

# ゲートウェイ起動（全チャネル + WebSocket サーバー）
picoclaw gateway

# デバッグモードで起動（詳細ログ出力）
picoclaw gateway --debug

# バージョン確認
picoclaw version

# ── 設定確認 ──────────────────────────────

# 設定ファイルの場所
ls ~/.picoclaw/config.json

# ワークスペースの場所
ls ~/.picoclaw/workspace/

# SOUL.md の確認
cat ~/.picoclaw/workspace/SOUL.md

# ── ログ確認（systemd 運用時）──────────────

# PicoClaw リアルタイムログ
journalctl -u picoclaw -f

# AITuber Kit リアルタイムログ
journalctl -u aituber-kit -f

# PicoClaw 最新50行
journalctl -u picoclaw -n 50

# ── サービス管理 ──────────────────────────

# 起動 / 停止 / 再起動
sudo systemctl start picoclaw
sudo systemctl stop picoclaw
sudo systemctl restart picoclaw

# 状態確認
sudo systemctl status picoclaw
sudo systemctl status aituber-kit

# 自動起動の有効化 / 無効化
sudo systemctl enable picoclaw
sudo systemctl disable picoclaw

# ── ヘルスチェック ────────────────────────

# PicoClaw 全体
curl http://localhost:18790/health

# AITuber WebSocket サーバー（接続クライアント数も表示）
curl http://localhost:8000/health/aituber

# ── 手動起動（systemd 未使用時）───────────

# フォアグラウンドで実行
/home/pi/picoclaw gateway

# バックグラウンドで実行
nohup /home/pi/picoclaw gateway > /home/pi/picoclaw.log 2>&1 &

# AITuber Kit 手動起動
cd /home/pi/aituber-kit
PORT=3000 HOSTNAME=0.0.0.0 npm start
```

## 更新手順

```bash
# PicoClaw の更新
sudo systemctl stop picoclaw
# 新しいバイナリを転送 or ビルド
sudo systemctl start picoclaw

# AITuber Kit の更新
sudo systemctl stop aituber-kit
cd /home/pi/aituber-kit
git pull
npm install
npm run build
sudo systemctl start aituber-kit
```

## 自動起動の確認

```bash
# RPi 再起動後に自動起動するか確認
sudo reboot

# 再起動後
systemctl is-active picoclaw      # → active
systemctl is-active aituber-kit   # → active
```
