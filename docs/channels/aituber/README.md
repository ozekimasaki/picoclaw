# AITuber Kit チャネル

PicoClaw と [AITuber Kit](https://github.com/tegnike/aituber-kit) を統合し、YouTube Live 配信でAIキャラクターがリスナーのコメントに応答する仕組みを提供します。

## アーキテクチャ

```
┌─────────────────────────────────────────┐
│ RPi 4 / サーバー (ヘッドレス)             │
│                                         │
│  PicoClaw gateway                       │
│    ├─ YouTube Live Chat ポーリング       │
│    ├─ LLM API 呼び出し                  │
│    └─ WebSocket Server (0.0.0.0:8000)   │
│                                         │
│  AITuber Kit Next.js (port 3000)        │
└────────────────┬────────────────────────┘
                 │ LAN
┌────────────────┴────────────────────────┐
│ 別PC (ブラウザ)                          │
│  http://<server>:3000                   │
│    └─ キャラクター描画 + TTS 再生         │
│    └─ ws://<server>:8000/ws             │
└─────────────────────────────────────────┘
```

## メッセージフロー

1. YouTube Live Chat API → `pollOnce()` でコメント取得
2. `preFilter()` → NGワード・短文・URL・連続文字を除外
3. `selectComments()` → 最新/優先/ランダムで選択（最大 N 件）
4. `batchAndHandle()` or `processMessage()` → LLM に送信
5. LLM が `[happy] 応答テキスト` 形式で回答
6. `parseEmotion()` → 感情タグを抽出
7. WebSocket で AITuber Kit に送信 `{text, emotion, role, type}`
8. AITuber Kit がキャラ表示 + TTS 再生
9. TTS 完了後 `{"type":"tts_complete"}` を送信
10. 次のメッセージを送信

## セットアップ

### 1. config.json

```json
{
  "channels": {
    "youtube": {
      "enabled": true,
      "api_key": "YOUR_YOUTUBE_API_KEY",
      "video_id": "YOUR_VIDEO_ID",
      "poll_interval_seconds": 20,
      "forward_channel": "aituber",
      "forward_chat_id": "default",
      "ng_words": ["spam", "荒らし"],
      "min_message_length": 2,
      "max_repeat_ratio": 0.7,
      "block_urls": true,
      "max_comments_per_poll": 3,
      "selection_strategy": "latest",
      "batch_comments": false
    },
    "aituber": {
      "enabled": true,
      "ws_host": "0.0.0.0",
      "ws_port": 8000,
      "ws_path": "/ws",
      "default_emotion": "neutral",
      "max_queue_size": 10
    }
  }
}
```

### 2. SOUL.md

`docs/channels/aituber/SOUL-template.md` を参考に、ワークスペースの `SOUL.md` に感情タグ出力の指示を追加してください。

LLM の応答は `[emotion] テキスト` 形式で出力されるようにします:
```
[happy] ありがとう！嬉しいです！
```

使用可能な感情タグ: `neutral`, `happy`, `sad`, `angry`, `relaxed`, `surprised`

### 3. AITuber Kit

AITuber Kit の外部連携モードを有効にします:

```env
# aituber-kit/.env
NEXT_PUBLIC_EXTERNAL_LINKAGE_MODE=true
NEXT_PUBLIC_WS_URL=ws://<server-ip>:8000/ws
```

**注意**: `NEXT_PUBLIC_*` は Next.js のビルド時に埋め込まれます。URL を変更した場合は再ビルドが必要です。

## 設定リファレンス

### YouTube フィルタリング

| 設定 | 型 | デフォルト | 説明 |
|---|---|---|---|
| `ng_words` | string[] | `[]` | NGワードリスト（大文字小文字無視） |
| `min_message_length` | int | `0` | 最小文字数（rune 長） |
| `max_repeat_ratio` | float | `0.0` | 最頻文字の出現率上限（0.0=無効） |
| `block_urls` | bool | `false` | URL を含むコメントをブロック |
| `max_comments_per_poll` | int | `3` | ポーリングあたりの最大コメント数 |
| `selection_strategy` | string | `"latest"` | `latest` / `priority` / `random` |
| `batch_comments` | bool | `false` | 複数コメントを1つにまとめて LLM に送信 |

### AITuber 設定

| 設定 | 型 | デフォルト | 説明 |
|---|---|---|---|
| `ws_host` | string | `"0.0.0.0"` | WebSocket サーバーのホスト |
| `ws_port` | int | `8000` | WebSocket サーバーのポート |
| `ws_path` | string | `"/ws"` | WebSocket エンドポイントパス |
| `default_emotion` | string | `"neutral"` | 感情タグなし時のデフォルト感情 |
| `max_queue_size` | int | `10` | 送信キューの最大サイズ |

### コメント選択戦略

- **`latest`**: 最新の N 件を選択
- **`priority`**: スーパーチャット → チャンネルオーナー → モデレーター → 一般の優先順
- **`random`**: ランダムに N 件を選択

## Raspberry Pi 4 デプロイ

RPi 4 ではヘッドレスサーバーとして運用し、別 PC のブラウザからアクセスします。

### 推奨設定（RPi 4, 4GB）

```json
{
  "channels": {
    "youtube": {
      "poll_interval_seconds": 30,
      "max_comments_per_poll": 2,
      "batch_comments": true
    },
    "aituber": {
      "ws_host": "0.0.0.0",
      "ws_port": 8000,
      "max_queue_size": 5
    }
  }
}
```

### systemd サービス

```bash
# /etc/systemd/system/picoclaw.service
[Unit]
Description=PicoClaw AI Assistant
After=network-online.target

[Service]
Type=simple
User=pi
ExecStart=/home/pi/picoclaw gateway
Restart=always

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable picoclaw
sudo systemctl start picoclaw
```

### ブラウザからアクセス

```
http://raspberrypi.local:3000
```

## ヘルスチェック

```bash
curl http://localhost:8000/health/aituber
# {"status":"ok","clients":1}
```

## Docker Compose

```bash
docker compose --profile gateway --profile aituber up
```

## トラブルシューティング

| 症状 | 原因 | 対策 |
|---|---|---|
| WebSocket 接続できない | CORS / ポート | `ws_host: "0.0.0.0"` 確認 |
| キャラが喋らない | TTS 未設定 | AITuber Kit の TTS 設定確認 |
| コメントが反映されない | YouTube API キー無効 | API キー・Video ID 確認 |
| 応答に感情がつかない | SOUL.md 未設定 | 感情タグ出力の指示を追加 |
| TTS 完了後に次が来ない | コールバック未実装 | AITuber Kit 側の TTS 完了通知を確認 |
