[English](./LISTENER.md) · **繁體中文**

# Listener

`Listener` 替你跑 read loop。它會讀取 frame、擋下 RFC 6455 不允許的 frame、把同一個
message 的 frame 組起來，再依 frame 的種類呼叫你的 hook。

它會依 RFC 檢查 frame，但不決定要怎麼處理。這由你的 hook 決定。

取得 `Conn` 的方法見 [SERVER.zh-TW.md](./SERVER.zh-TW.md) 與
[CLIENT.zh-TW.md](./CLIENT.zh-TW.md)。傳送見 [SENDING.zh-TW.md](./SENDING.zh-TW.md)。
讀取 message 的內容見 [READING.zh-TW.md](./READING.zh-TW.md)。

## Contents

- [Quick start](#quick-start) — 從頭到尾處理一條連線
- [標準 Listener](#標準-listener) — `conn.NewStandardListener()`：它設定了什麼，以及怎麼擴充它
- [純 Listener 不會替你做的三件事](#純-listener-不會替你做的三件事) — [關閉](#它不會關閉連線) · [寫出](#它不會寫出任何東西) · [存活偵測](#它沒有-ping-pong-存活偵測)
- [Configuration](#configuration) — `NewListenerConfig`、`SetConfig`，以及上限
- [Hooks](#hooks) — 哪種 frame 交給哪個 hook，以及 RFC 要求你做什麼
- [Errors](#errors) — 四種 error，以及哪幾種要回 close frame · [1. 某個 frame 違反規則](#1-某個-frame-違反規則) · [2. 連線斷了](#2-連線斷了) · [3. Listener 用錯了](#3-listener-用錯了) · [4. 其他 error](#4-其他-error) · [完整的處理方式](#完整的處理方式)
- [暫停與繼續](#暫停與繼續) — 結束一次執行，再繼續
- [Concurrency](#concurrency) — 只用一個讀取者

## Quick start

`conn` 是完成 handshake 後的 `*wlgows.Conn`。這個例子是 server 端的。程式會處理一條連線，
以及 `Listen` 所有可能的回傳結果。

`readMessage` 與 `maxMessageSize` 來自
[READING 的 Quick start](./READING.zh-TW.md#quick-start)。`readMessage` 讀出一個
message 的內容；如果對方壓縮了這個 message，它也會解壓縮。

```go
func handleConn(conn *wlgows.Conn) {
	defer conn.Close() // Listener 不會關閉任何東西，所以由你關閉

	listener := wlgows.NewListener(conn, wlgows.NewListenerConfig{
		PeerIsClient:                  true, // 我們是 server，所以對方是 client
		PermessageDeflateAgreedConfig: conn.GetPermessageDeflateAgreedConfig(),
	})

	buf := make([]byte, maxMessageSize) // 所有 message 共用一個 buffer

	listener.SetConfig(wlgows.ListenerConfig{
		MaxDataFramesSize: 10 * 1024 * 1024, // 每個 message 最多 10 MB，以收到時的大小計算
		MaxFrameSize:      10 * 1024 * 1024, // 單一 frame，包含 header
		MaxDataFrameCount: 4000,             // 見 Configuration
		FrameReadTimeout:  60 * time.Second,

		Text: func(frames wlgows.DataFrames) {
			message, err := readMessage(conn, frames, buf)
			if err != nil {
				listener.PauseListen(err) // Listen 會回傳 err
				return
			}
			log.Printf("text: %s", message)
		},
		Binary: func(frames wlgows.DataFrames) {
			message, err := readMessage(conn, frames, buf)
			if err != nil {
				listener.PauseListen(err)
				return
			}
			log.Printf("binary: %d bytes", len(message))
		},
		Ping: func(frame *wlgows.Frame) {
			conn.SendPong(frame.PayloadData) // 5.5.2、5.5.3：回一個 pong，payload 相同
		},
		Close: func(frame *wlgows.Frame) {
			payload, _ := frame.GetClosePayload() // Listener 已經檢查過了
			conn.SendClose(payload)               // 5.5.1：回一個 close，然後停止
			listener.PauseListen(nil)             // nil：Listen 回傳 nil
		},
		Unknown: func(frame *wlgows.Frame) {
			// 5.2 保留的 opcode：回 close 1002，然後停止。
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen(wlgows.ErrReservedOpcode) // Listen 會回傳這個 error
		},
	})

	// 一直等到讀取失敗、有 frame 違反規則，或有人呼叫 PauseListen。
	switch err := listener.Listen(); {
	// PauseListen(nil)。這裡只有 Close hook 會這樣呼叫：對方關閉了連線。
	case err == nil:

	// 還沒讀任何 byte 就回傳了。連線本身沒有問題。
	case errors.Is(err, wlgows.ErrListenerConnIsNil),
		errors.Is(err, wlgows.ErrListenerIsListening):
		log.Println("listener misuse:", err)

	// 有 frame 或 message 違反規則、連線斷了，或其他原因。只有第一種會拿到
	// close payload。
	default:
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload) // 如果 hook 已經送過 close，這裡什麼都不做（ErrCloseAlreadySent）
		}
		log.Println("closing:", err)
	}
}
```

Listener 從不寫出，也從不關閉。上面每一個 `Send*` 都是你自己呼叫的。`Pong` 刻意不設：
5.5.3 不要求回應 pong，而 nil hook 什麼都不做。

`conn.NewStandardListener()` 會替你設好相同的 hook 與上限。見
[標準 Listener](#標準-listener)。

## 標準 Listener

`conn.NewStandardListener()` 已經回應了 RFC 6455 要求讀取端回應的每一種 frame。你還要
做兩件事：關閉連線，以及針對 `Listen` 回傳的 error 送出 close frame。

從這裡開始，再加上你的 message hook：

```go
listener := conn.NewStandardListener()

buf := make([]byte, maxMessageSize) // 所有 message 共用一個 buffer

config := listener.GetConfig() // 從它設好的設定開始：SetConfig 會整個取代
config.MaxDataFramesSize = 10 * 1024 * 1024
config.MaxFrameSize = 10 * 1024 * 1024
config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
config.Text = func(frames wlgows.DataFrames) {
	message, err := readMessage(conn, frames, buf) // 來自 READING 的 Quick start
	if err != nil {
		listener.PauseListen(err)
		return
	}
	log.Printf("text: %s", message)
}
listener.SetConfig(config)

err := listener.Listen()
```

它設定了：

| 項目 | 設為 | 作用 |
|---|---|---|
| `PeerIsClient` | 依 `conn` 是 server 或 client 自動設定 | 預期對方用正確的 mask（5.1） |
| `PermessageDeflateAgreedConfig` | handshake 談好的設定 | 讓 Listener 接受帶有 RSV1 的壓縮 message（[RFC 7692](./PERMESSAGE_DEFLATE.zh-TW.md)） |
| `Ping` | `conn.RFC6455PingHook` | 回一個 payload 相同的 pong（5.5.2、5.5.3） |
| `Pong` | `conn.RFC6455PongHook` | 什麼都不做：pong 不需要回應（5.5.3） |
| `Close` | `conn.RFC6455CloseHook(listener)` | 回送相同的 close，除非這一端先送過。然後以 `nil` 停止 `Listen`（5.5.1） |
| `Unknown` | `conn.RFC6455UnknownHook(listener)` | 送出 close code 1002 的 close frame，然後以 `ErrReservedOpcode` 停止 `Listen`（5.2、7.4.1） |
| `MaxDataFramesSize` | `StandardMaxDataFramesSize`，4 KB | 更大的 message 會讓 `Listen` 以 error 結束。`StandardClosePayloadFor` 把它對應到 1009 |
| `MaxFrameSize` | `StandardMaxFrameSize`，4 KB | 單一 frame，包含 header。要和 `MaxDataFramesSize` 一起調高，否則只有一個 frame 的大 message 會被拒絕 |
| `MaxDataFrameCount` | `StandardMaxDataFrameCount`，16 | 由 `MaxDataFramesSize` 算出來，所以兩者要一起調高 |
| `FrameReadTimeout` | `StandardFrameReadTimeout`，60 秒 | 對方 60 秒都沒送東西時，結束 `Listen`。請每 30 秒以內送一次 ping |

留給你的：`Text`、`Binary` 或 `Data` hook，以及 `Listen` 回傳後關閉連線。

**每一項都可以改。** 想在標準 hook 上加東西，先把它存到變數，再從你自己的 hook 呼叫它：

```go
standardClose := config.Close
config.Close = func(frame *wlgows.Frame) {
	payload, _ := frame.GetClosePayload()
	log.Printf("peer closed: %+v", payload)
	standardClose(frame)
}
```

這些 hook 都是 exported 的。用 `wlgows.NewListener` 建立的 Listener 也可以使用它們，例如
`Ping: conn.RFC6455PingHook`。

## 純 Listener 不會替你做的三件事

純 Listener 是指用 `wlgows.NewListener` 建立、沒有設任何 hook 的 Listener。標準
Listener 的 hook 會替你處理寫出。關閉與存活偵測永遠要你自己做。

### 它不會關閉連線

收到 close frame、發生 error、message 超過上限、遇到未知的 opcode，它都不會關閉連線。
它只會回傳，由**你**關閉。

RFC 6455 5.5.1 要求兩端在不同時間關閉。server MUST 在 close frame 之後馬上關閉。
client SHOULD 等 server 先關閉。Listener 不知道自己在哪一端，所以由你的程式關閉。

client 要等的話：在 close frame 之後繼續讀取，並設一個 deadline，直到讀取失敗，再關閉。
在 TLS 連線上呼叫 `conn.Close()` 也會結束 TLS session，這是 7.1.1 的要求。

### 它不會寫出任何東西

不回 pong、不回 close、不 echo。Quick start 裡的每一個 `Send*` 都是你的程式自己呼叫的。

所以**沒有 hook 的 Listener 什麼都不回應**。對方會一直等不到 pong。保留的 opcode 會被
丟掉，Listener 繼續讀取，但依 5.2，這時應該讓連線失敗。

### 它沒有 ping pong 存活偵測

`Ping` 與 `Pong` hook 只告訴你有 frame 到了。對方已經斷線，和對方只是沒送東西，看起來一模一樣。
要發現對方斷線，你需要自己的計時器：

- 固定間隔送 ping：[`StartPingLoop`](./SENDING.zh-TW.md#定期送出-ping) 會幫你做
- 收到**任何** pong 就重設 deadline，不管它的 payload
- deadline 到了還沒收到 pong，代表對方已經不在了：關閉連線

在 `Pong` hook 記下時間，deadline 到了就關閉：

```go
var lastPong atomic.Value // time.Time
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

不要檢查 payload。RFC 6455 5.5.3 允許對方送出沒人要求的 pong，也允許收到多個 ping 時，
只回應最後一個。收到任何 pong，就代表對方還在。

`FrameReadTimeout` 不負責這件事。它只在*時間內沒有收到一個完整的 frame* 時結束
`Listen`。對方只送資料、從不回 ping，看起來還是活著的。

## Configuration

Listener 有兩份設定：

| 設定 | 由誰設定 | 能不能改 |
|---|---|---|
| `NewListenerConfig` | `wlgows.NewListener(conn, config)` | 不能：Listener 存在期間都固定 |
| `ListenerConfig` | `listener.SetConfig(config)` | 能：任何時候、任何 goroutine 都可以 |

### `NewListenerConfig`

| 項目 | 零值 | 說明 |
|---|---|---|
| `PeerIsClient` | 對方是 **server** | 對方是不是 client。這會決定對方的 frame 該不該有 mask（5.1） |
| `PermessageDeflateAgreedConfig` | 沒有談好 permessage-deflate | 傳入 `conn.GetPermessageDeflateAgreedConfig()` |

**`PeerIsClient` 一定要設對。** 它的零值表示對方是 server。server 沒設的話，會拒絕
client 送來的每一個 frame。

**記得傳入 `PermessageDeflateAgreedConfig`。** 它是 nil 時，壓縮的 message 帶有
RSV1，Listener 會以 `ErrReservedBitsSet` 拒絕。

`conn.NewStandardListener()` 會從連線設好這兩項。

### `ListenerConfig`

`SetConfig` 會加 lock，複製整份 `ListenerConfig`。你可以在 `Listen` 之前、兩次執行之間，
或在 hook 裡呼叫它。

| 項目 | 型別 | 零值 | 說明 |
|---|---|---|---|
| `MaxDataFramesSize` | `uint64` | 沒有上限 | 一個 message 所有 frame 收到時的總 byte 數，包含 header。每讀到一個 header 就檢查一次 |
| `MaxDataFrameCount` | `uint64` | 沒有上限 | 一個 message 最多幾個 frame |
| `MaxFrameSize` | `uint64` | 沒有上限 | 一個 data frame 或保留 opcode frame 的 byte 數，包含 header。讀到它的 header 時就檢查 |
| `FrameReadTimeout` | `time.Duration` | 沒有 timeout | 讀取一個 frame 的時間上限 |
| `Ping` | `func(*Frame)` | frame 被丟掉 | [5.5.2、5.5.3](#hooks)：回一個 payload 相同的 pong |
| `Pong` | `func(*Frame)` | frame 被丟掉 | [5.5.3](#hooks)：不需要回應，所以 nil 也沒問題 |
| `Close` | `func(*Frame)` | frame 被丟掉 | [5.5.1](#hooks)：除非這一端送過，否則回一個 close，然後 `PauseListen` |
| `Text` | `func(DataFrames)` | message 被丟掉 | 一個完整的 text message |
| `Binary` | `func(DataFrames)` | message 被丟掉 | 一個完整的 binary message |
| `Data` | `func(*Frame)` | 使用 `Text` 與 `Binary` | [每一個 data frame](#data-hook)，一個都不保留 |
| `Unknown` | `func(*Frame)` | frame 被丟掉 | [5.2](#hooks) 保留的 opcode：回 1002，然後停止 |

**它會整個取代。** 沒填的項目會變成零值。只想改一項，先取出目前的設定：

```go
config := listener.GetConfig() // 目前使用中的設定的副本
config.MaxDataFrameCount = 4000
listener.SetConfig(config)
```

在 hook 裡改設定也是一樣。新設定從下一個 frame 開始生效，不影響目前這個。

每個零值都能用，但**一定要設 `MaxDataFramesSize` 與 `MaxFrameSize`。** 一個 10 byte
的 header 可以宣稱 10 GB 的 payload。沒有上限的話，就會配置 10 GB 的記憶體。保留
opcode 的 frame 只受 `MaxFrameSize` 限制。

`MaxDataFramesSize` 算的是收到時的 byte 數。壓縮的 message 解壓縮後可能大很多。要限制
解壓縮後的大小，見 [限制大小](./READING.zh-TW.md#限制大小)。

**怎麼選 `MaxDataFrameCount`。** 一個 message 分成幾個 frame 是對方決定的。從你接受的
最小 frame 算起：

	MaxDataFrameCount = MaxDataFramesSize / 你接受的最小 frame

10 MB 分成 4 KB 的 frame 是 2560 個，所以 4000 還有餘裕。寧可設高，不要設低。設太高
代價很小，因為 `MaxDataFramesSize` 已經限制了記憶體。設太低會拒絕正常的 message。

為什麼要限制數量：一個空的 frame 只佔 `MaxDataFramesSize` 2 byte（client 送來的是
6 byte），卻要佔用一整個 `Frame` 的記憶體。

**用 `Data` hook 時，`MaxDataFramesSize` 要容得下整個 stream**，而不是一個 frame。
見 [Data hook](#data-hook)。

每一項的完整說明：

```bash
go doc github.com/weilun-shrimp/wlgows/v7.ListenerConfig
```

## Hooks

hook 在 read loop 裡一次只跑一個。hook 執行時，**read loop 會停下來等它跑完**：
不會讀任何 frame，ping 也不會。慢的工作請交給 goroutine 或 queue。hook 從不同時執行，所以 message 依序
到達（5.4）。

nil hook 會丟掉它的 frame。

| Hook | 收到 | 要你做什麼 |
|---|---|---|
| `Ping` | 一個 frame | 5.5.2、5.5.3：MUST 回一個 payload 相同的 pong，除非已經收到 close |
| `Pong` | 一個 frame | 5.5.3：不需要回應 |
| `Close` | 一個 frame | 5.5.1：除非這一端已經送過，否則 MUST 回一個 close，然後關閉。`Frame.GetClosePayload` 會給你 status code 與 reason，兩者都已經檢查過。client 不可以把 reason 顯示給使用者 |
| `Text` | 一個完整的 message | 用 [`DataFramesReader`](./READING.zh-TW.md#dataframesreader) 讀取 |
| `Binary` | 一個完整的 message | 用 [`DataFramesReader`](./READING.zh-TW.md#dataframesreader) 讀取 |
| `Data` | 一個 data frame | **小心使用。** 見 [Data hook](#data-hook) |
| `Unknown` | 一個 frame | 5.2 保留的 opcode。這是 protocol error：回 1002 並關閉 |

### `Text` 與 `Binary`

`Text` 與 `Binary` 收到的是一個完整的 message，型別是 `DataFrames`：這個 message 的
每一個 frame，依序排好。夾在 message 兩個 frame 中間的 control frame 會交給它自己的
hook，不會打斷這個 message。

用 `conn.DataFramesReader(frames)` 讀出內容。它會把 payload 接起來、解壓縮壓縮過的
message，並檢查 text 是不是合法的 UTF-8。見 [READING.zh-TW.md](./READING.zh-TW.md)。

text 的 UTF-8 檢查（5.6）：

| text message 是 | 由誰檢查 |
|---|---|
| 沒有壓縮 | Listener，在 `Text` 執行之前 |
| 有壓縮 | `DataFramesReader`，在解壓縮之後 |

Listener 檢查的是**整個 message**，而不是每個 frame，因為一個 frame 可能在某個字元的
中間結束。`Binary` 不做 UTF-8 檢查。

### Data hook

設了 `Data` 之後，Listener 不會把 frame 組成 message。每一個 data frame 直接交給
`Data`，一個都不保留，`Text` 與 `Binary` 也不會執行。只在 message 大到放不進記憶體時
才用它。放得下的 message，請用 `Text` 或 `Binary`。

用 `Data` 時，你的程式要多做這些事：

- 注意 `frame.FIN`。沒有其他東西會標出 message 的結尾。
- continuation frame 不會標示 message 是 text 還是 binary。要知道是哪一種，請呼叫
  `listener.GetCurrentMsgOpcode()`。
- 呼叫 `listener.IsCurrentMsgDeflated()` 確認 message 有沒有壓縮。
- 自己檢查 text 的 UTF-8。單一 frame 無法單獨檢查。
- 上限仍然有效，空的 frame 也還是會收到。
- 在兩個 message 之間設定 `Data`，不要在 message 進行中設定。

`MaxDataFramesSize` 涵蓋整個 message，所以要容得下整個 stream。這樣一來，單一 frame
也可能用掉整個上限。所以也要設 `MaxFrameSize`：它限制每個 frame 的大小。

壓縮與沒壓縮的例子都在
[大型 message：Data hook](./READING.zh-TW.md#大型-messagedata-hook)。

### 收到 close frame 後停止讀取

RFC 6455 1.4（非規範性）說，對方會丟掉 close 之後收到的任何資料。Listener 在 `Close`
之後還會繼續讀取，所以你的 `Close` hook 一定要呼叫 `PauseListen`。不呼叫的話，之後的
message 還是會送到 `Text` 或 `Binary`。

## Errors

呼叫 `PauseListen(nil)` 之後，如果沒有其他問題，`Listen` 回傳 `nil`。
`PauseListen(err)` 會讓 `Listen` 回傳 `err`，讓你說明停止的原因，例如服務要關閉。
第一次暫停才算數，之後的會被忽略。

暫停之後，你常常會關閉連線，好結束卡住的讀取。這時讀取也會失敗。`Listen` 會把兩個
error **合併**回傳，`errors.Is` 兩個都找得到。所以有 error 不一定代表對方做錯了什麼。

`StandardClosePayloadFor` 不認得你自己的 error，對它們會回傳 `nil`（見第 4 種）。如果你的
error 需要 close code，請在呼叫 `StandardClosePayloadFor` 之前先自己判斷。

`io.EOF` 不代表正常關閉。它表示對方**沒有**送 close frame 就斷了 TCP 連線。7.4.1 把這
叫做 1006。

error 分成四種，每一種要用不同的方式處理。

### 1. 某個 frame 違反規則

對方違反了 RFC 6455。回一個 close frame，然後關閉。`StandardClosePayloadFor` 依 7.4.1
給出 close code：

| Error | Close code |
|---|---|
| `ErrReservedBitsSet` | 1002 |
| `ErrFrameNotMasked`、`ErrFrameMasked` | 1002 |
| `ErrReservedOpcode` | 1002 |
| `ErrControlFrameFragmented` | 1002 |
| `ErrControlFramePayloadTooLong` | 1002 |
| `ErrClosePayloadTooShort` | 1002 |
| `ErrPayloadLengthMSBSet` | 1002 |
| `ErrPayloadLengthNotMinimal` | 1002 |
| `ErrContinuationFrameWithoutMsg`、`ErrDataFrameDuringMsg` | 1002 |
| `ErrInvalidCloseStatusCode` | 1002 |
| `ErrInvalidUTF8` | 1007 |
| `ErrFrameByteLengthExceeded` | 1009 |
| `ErrDataFrameCountExceeded` | 1009 |

讀取 message 也可能失敗：`ErrInvalidUTF8`（1007）、`ErrInflateDataInvalid`（1007）或
`ErrSizeLimitExceeded`（1009）。你的 hook 把它們交給 `PauseListen`，`Listen` 就會回傳
它們。`StandardClosePayloadFor` 也認得它們。見 [READING 的 Errors](./READING.zh-TW.md#errors)。

**遇到這些 error 後，絕對不要再讀取。** 7.1.7 規定連線失敗後，不可以再處理對方送來的
任何東西。而且其中幾種發生時，被拒絕的 frame 只讀了一部分，下一次讀取會從它的中間開始。
寫出還是可以的，所以 close frame 送得出去。

### 2. 連線斷了

`io.EOF`、`io.ErrUnexpectedEOF`、`os.ErrDeadlineExceeded`、`net.ErrClosed`、
connection reset。這些來自 socket，對方沒有違反任何規則。已經沒有對象可以送 close frame
了。7.4.1 把這叫做 **1006**，並且禁止把它送出去。所以記下 log，然後關閉。

read timeout 來自你自己設的 `FrameReadTimeout`，不是對方的錯。連線通常還能寫出，所以你
*可以*先送 1000 或 1001。這由你決定，所以 `StandardClosePayloadFor` 回傳 `nil`。

### 3. Listener 用錯了

- `ErrListenerConnIsNil`：呼叫 `NewListener` 時，傳進去的 `conn` 是 nil。
- `ErrListenerIsListening`：一個 `Listen` 還在執行時，又開始了第二個。

兩者都在讀取任何東西之前就回傳。

**遇到這兩種，不要關閉連線。** 遇到 `ErrListenerIsListening` 時，另一個 `Listen` 還在
這條連線上執行。這些是你程式裡的 bug，請修正它們。

### 4. 其他 error

來自你自己的 `net.Conn`、TLS 層或 mock 的一般 error。這個 package 無法判斷是誰的錯。

當成第 2 種處理：關閉，什麼都不送。7.1.7 說 close frame SHOULD 送出，不是 MUST，所以
不送並不違反規則。送出 1002 會怪罪一個可能沒做錯任何事的對方。如果你想送一個，1011 表示
問題出在這一端。如果你知道自己的 error 代表什麼，請在呼叫 `StandardClosePayloadFor`
**之前**，自己把它對應到 close code。

### 完整的處理方式

`StandardClosePayloadFor` 對第 1 種回傳 payload，對其他回傳 `nil`。`nil` 只代表「不送
close frame」。第 2 和第 4 種還是要關閉連線。第 3 種不可以關閉：

```go
err := listener.Listen()
switch {
case err == nil: // PauseListen(nil)：沒有要回應的

case errors.Is(err, wlgows.ErrListenerConnIsNil),
	errors.Is(err, wlgows.ErrListenerIsListening):
	log.Printf("listener misuse: %v", err) // 第 3 種：不要關閉這條連線

default:
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		// 第 1 種：關閉之前先送出它
	}
	// 第 1、2、4 種都要關閉連線。對第 1 種來說，7.1.7 規定這是 MUST。
	// 送出與關閉都由你的程式呼叫。
}
```

payload 裡的 `Reason` 是空的。想告訴對方原因，請在送出前設定 `Reason`。

保留的 opcode（5.2）會交給 `Unknown` hook，不會交給 `Listen`。標準的 hook 會回 1002，
並以 `ErrReservedOpcode` 停止。用你自己的 hook 時，這兩件事都由你的程式做。nil hook 會
把它丟掉，和其他 nil hook 一樣。

## 暫停與繼續

`PauseListen(err)` 結束這次執行，`Listen` 回傳 `err`。傳 `nil` 可以不帶 error 停止。
任何 goroutine 都可以呼叫它。`Listen` 沒在執行時，呼叫它不會有任何作用。

Listener 只在**兩個 frame 之間**才會發現暫停。在等待下一個 frame 的期間，暫停還不會
生效。如果對方都不送東西，讀取會一直卡住。只有關閉連線才能讓它結束。

再呼叫一次 `Listen`，會從停下的地方繼續。還沒收齊所有 frame 的 message 會保留下來，所以
你可以在中間改設定，不會漏掉 frame。

## Concurrency

**只從一個 goroutine 讀取 frame。** lock 可以避免兩個讀取者把 byte 混在一起，但每個讀取
者拿到的 frame 還是隨機的。Listener 本身就是一個讀取者，所以它在執行時，不要自己讀取
frame。

`conn.Read` 來自內嵌的 `net.Conn`，不會經過 lock。請透過 Listener 讀取 frame。

不用 Listener 讀取，以及 read buffer 的記憶體，見
[READING.zh-TW.md](./READING.zh-TW.md#不用-listener-讀取)。
