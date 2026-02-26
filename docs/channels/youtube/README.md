# YouTube Live Chat ブリッジ

YouTube Live Chat のコメントをリアルタイムで取得し、別のチャネル（Discord、Telegram 等）へ転送するブリッジ機能です。

## 設定

```json
{
  "channels": {
    "youtube": {
      "enabled": true,
      "api_key": "YOUR_YOUTUBE_API_KEY",
      "video_id": "xxxxxxxxxx",
      "poll_interval_seconds": 30,
      "forward_channel": "discord",
      "forward_chat_id": "123456789",
      "message_format": "[YT] {author}: {message}",
      "allow_from": []
    }
  }
}
```

| フィールド | 型 | 必須 | デフォルト | 説明 |
|---|---|---|---|---|
| enabled | bool | ○ | false | YouTube チャネルの有効/無効 |
| api_key | string | ○ | - | YouTube Data API v3 キー |
| video_id | string | ○ | - | 監視対象ライブ配信の動画ID |
| poll_interval_seconds | int | - | 20 | ポーリング間隔（秒）。最小5秒 |
| forward_channel | string | ○ | - | 転送先チャネル名（`discord`, `telegram` 等） |
| forward_chat_id | string | ○ | - | 転送先のチャット/チャンネルID |
| message_format | string | - | `[YT] {author}: {message}` | 転送メッセージのフォーマット |
| allow_from | array | - | [] | YouTube チャンネルIDの許可リスト（空=全員許可） |

## セットアップ手順

### 1. Google Cloud Console でプロジェクトを作成

1. [Google Cloud Console](https://console.cloud.google.com/) にアクセス
2. 新しいプロジェクトを作成（または既存のプロジェクトを選択）

### 2. YouTube Data API v3 を有効化

1. [API ライブラリ](https://console.cloud.google.com/apis/library) を開く
2. 「YouTube Data API v3」を検索
3. 「有効にする」をクリック

### 3. API キーを作成

1. [認証情報](https://console.cloud.google.com/apis/credentials) ページを開く
2. 「認証情報を作成」→「API キー」を選択
3. 作成されたキーの「キーを制限」をクリック
4. 「API の制限」で「YouTube Data API v3」のみを選択（推奨）
5. キーをコピー

### 4. 動画IDの確認

YouTube ライブ配信の URL から動画IDを取得します:

```
https://www.youtube.com/watch?v=xxxxxxxxxx
                                ^^^^^^^^^^
                                この部分が video_id
```

### 5. 転送先チャネルの設定

転送先チャネル（Discord や Telegram 等）が既に有効化されている必要があります。

- **Discord**: `forward_chat_id` にはチャンネルID を設定（チャンネルを右クリック →「IDをコピー」）
- **Telegram**: `forward_chat_id` にはチャットID を設定

### 6. 設定ファイルに追記

`~/.picoclaw/config.json` の `channels` セクションに YouTube の設定を追加します。

### 7. 動作確認

```bash
picoclaw gateway
```

起動後、以下のログが表示されれば接続成功です:

```
YouTube channel enabled successfully
Connected to live chat (video_id=..., live_chat_id=...)
```

## API クォータについて

- YouTube Data API v3 のデフォルトクォータ: **10,000 ユニット/日**
- `liveChatMessages.list` の消費: 約 **5 ユニット/回**
- 推奨: `poll_interval_seconds` を **30 秒以上** に設定
  - 30秒間隔: 5 × 2,880 = 14,400 ユニット/日
  - 60秒間隔: 5 × 1,440 = 7,200 ユニット/日
- クォータの引き上げが必要な場合は [Google Cloud Console](https://console.cloud.google.com/apis/api/youtube.googleapis.com/quotas) から申請

## トラブルシューティング

### API キーエラー (401)

API キーが正しく設定されているか確認してください。

### ライブ配信が見つからない (404)

- `video_id` が正しいか確認
- 配信が開始されているか確認（予約配信の場合、配信開始前は接続できません）

### アクセス拒否 (403)

`liveChatMessages.list` は公開ライブ配信でも OAuth2 認証が必要な場合があります。
現在の実装は API キーのみ対応しています。この問題が発生した場合は Issue を作成してください。

### API クォータ超過 (403 quotaExceeded)

`poll_interval_seconds` を大きくするか、Google Cloud Console でクォータの引き上げを申請してください。

## メッセージフォーマット

`message_format` で転送メッセージの形式をカスタマイズできます。

| プレースホルダ | 説明 |
|---|---|
| `{author}` | コメント投稿者の表示名 |
| `{message}` | コメント本文 |

例:
- `[YT] {author}: {message}` → `[YT] UserName: こんにちは！`
- `YouTube | {author} said: {message}` → `YouTube | UserName said: こんにちは！`
