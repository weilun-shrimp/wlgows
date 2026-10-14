[English](./LISTENER_README.md) · **繁體中文**

# Listener

`Listener` 替你跑 read loop。它讀取 frame、擋下 RFC 6455 不允許的 frame、把分段的
message 組回來，再依 opcode 呼叫你的 hook。保留的 opcode 會交給你的 `Unknown` hook。

它依 RFC 檢查 frame，但不決定要怎麼處理。那由你的 hook 決定。

## Contents

- [Quick start](#quick-start) — 從頭到尾處理一條連線
- [標準 Listener](#標準-listener) — `conn.NewStandardListener()`：它設定了什麼，以及如何在它之上擴充
- [純 Listener 不會替你做的三件事](#純-listener-不會替你做的三件事) — [關閉](#它不會關閉連線) · [寫出](#它不會寫出任何東西) · [存活偵測](#它沒有-ping-pong-存活偵測)
- [Configuration](#configuration) — `SetConfig`、`GetConfig`，以及哪一項一定要設對
- [Hooks](#hooks) — 哪種 frame 會交給哪個 hook，以及 RFC 要求你做什麼 · [不複製地讀取 text：`unsafe`](#不複製地讀取-textunsafe)
- [Errors](#errors) — 四種 error，以及哪幾種要回 close frame · [1. frame 違反規則](#1-某個-frame-違反規則) · [2. 連線斷了](#2-連線斷了) · [3. 使用方式錯誤](#3-listener-使用方式錯誤) · [4. 其他 error](#4-其他-error) · [整理起來](#整理起來)
- [暫停與重新開始](#暫停與重新開始) — 結束一次執行，再重新開始
- [不用 Listener 讀取](#不用-listener-讀取) — `GetNextFrameHeader` 與 `ReadFramePayload`，一次一個 frame；不建議
- [記憶體與 read buffer](#記憶體與-read-buffer)
- [Concurrency](#concurrency) — 只用一個讀取者

取得 `Conn` 的方法見 [SERVER_README.zh-TW.md](./SERVER_README.zh-TW.md) 與
[CLIENT_README.zh-TW.md](./CLIENT_README.zh-TW.md)，傳送見
[SENDING_README.zh-TW.md](./SENDING_README.zh-TW.md)。

## Quick start

`conn` 是一條已經完成 handshake 的 `*wlgows.Conn`，可以來自
[server](./SERVER_README.zh-TW.md#handshake) 端或 [client](./CLIENT_README.zh-TW.md#handshake) 端。
這裡用的是 server 端的。它處理一條連線，以及 `Listen` 每一種回傳結果。`Pong` 刻意
不設：5.5.3 不要求回應 pong，而 nil hook 什麼都不做。

`SetConfig` 一次傳入完整的設定，什麼時候都可以呼叫：`Listen` 之前、兩次執行之間，
或在 hook 裡面都可以。

```go
func handleConn(conn *wlgows.Conn) {
	defer conn.Close() // 要由你關閉：Listener 不會關任何東西

	listener := wlgows.NewListener(conn)
	listener.SetConfig(wlgows.ListenerConfig{
		PeerIsClient:      true,             // 我們是 server，所以對方會 mask
		MaxDataFramesSize: 10 * 1024 * 1024, // 每個 message 最多 10 MB
		MaxFrameSize:      10 * 1024 * 1024, // 單一 frame，包含 header
		MaxDataFrameCount: 4000,             // 見 Configuration
		FrameReadTimeout:  60 * time.Second,

		Text: func(frames wlgows.DataFrames) {
			log.Printf("text: %s", frames.String())
		},
		Binary: func(frames wlgows.DataFrames) {
			log.Printf("binary: %d bytes", frames.ByteLen())
		},
		Ping: func(frame *wlgows.Frame) {
			conn.SendPong(frame.PayloadData) // 5.5.2、5.5.3：回一個 pong，payload 原樣帶回
		},
		Close: func(frame *wlgows.Frame) {
			payload, _ := frame.GetClosePayload() // Listener 已經檢查過了
			conn.SendClose(payload)               // 5.5.1：回一個 close，然後停止讀取
			listener.PauseListen(nil)             // nil：對方已經說明原因，Listen 回傳 nil
		},
		Unknown: func(frame *wlgows.Frame) {
			// 5.2 保留的 opcode：回 close 1002，然後停止。
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen(wlgows.ErrReservedOpcode) // Listen 會回傳這個 error
		},
	})

	// 會一直等，直到讀取失敗、有 frame 違反規則，或有人呼叫 PauseListen。
	switch err := listener.Listen(); {
	// PauseListen(nil)：這裡只有 Close hook 會這樣呼叫，代表對方關閉了連線，
	// 沒有其他要處理的。
	case err == nil:

	// 還沒讀任何 byte 就回傳了，所以和這條連線無關。
	case errors.Is(err, wlgows.ErrListenerConnIsNil),
		errors.Is(err, wlgows.ErrListenerIsListening):
		log.Println("listener misuse:", err)

	// 有 frame 違反規則、socket 斷了，或是無法判斷原因的 error。只有第一種會拿到
	// payload，socket 斷了就沒有東西可以回給對方。
	default:
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			conn.SendClose(payload) // 用它回一個 close，接著由 defer 關閉連線
		}
		log.Println("closing:", err)
	}
}
```

Listener 從不寫出也不關閉，所以上面每一個 `Send*` 都是你自己呼叫的。
`conn.NewStandardListener()` 會替你設好相同作用的 hook 與上限：見
[標準 Listener](#標準-listener)。`PauseListen` 結束這次執行，`Listen` 會回傳你給的原因。

## 標準 Listener

`conn.NewStandardListener()` 已經回應了 RFC 6455 要求讀取端回應的每一種 frame。
關閉連線，以及針對 `Listen` 回傳的 error 送出 close frame，仍由你負責。從這裡開始，
加上你的 message hook 即可：

```go
listener := conn.NewStandardListener()

config := listener.GetConfig() // 從它設好的設定開始：SetConfig 會整個取代
config.MaxDataFramesSize = 10 * 1024 * 1024
config.MaxFrameSize = 10 * 1024 * 1024
config.MaxDataFrameCount = 10 * 1024 * 1024 / 256
config.Text = func(frames wlgows.DataFrames) {
	log.Print(frames.String())
}
listener.SetConfig(config)

err := listener.Listen()
```

它設定了：

| 項目 | 設為 | 作用 |
|---|---|---|
| `PeerIsClient` | `conn` 的另一端 | 正確預期對方的 mask（5.1） |
| `Ping` | `conn.RFC6455PingHook` | 用帶著相同 payload 的 pong 回應（5.5.2、5.5.3） |
| `Pong` | `conn.RFC6455PongHook` | 什麼都不做：pong 不需要回應（5.5.3） |
| `Close` | `conn.RFC6455CloseHook(listener)` | 回送 close（除非這一端先送了），再以 `nil` 停止 `Listen`（5.5.1） |
| `Unknown` | `conn.RFC6455UnknownHook(listener)` | 送出 1002 的 close，再以 `ErrReservedOpcode` 停止 `Listen`（5.2、7.4.1） |
| `MaxDataFramesSize` | `StandardMaxDataFramesSize`，4 KB | 超過的 message 會讓 `Listen` 以 error 結束，`StandardClosePayloadFor` 會把它對應到 1009 |
| `MaxFrameSize` | `StandardMaxFrameSize`，4 KB | 單一 frame，包含 header；要和大小一起調高，否則只有一個 frame 的較大 message 會被拒絕 |
| `MaxDataFrameCount` | `StandardMaxDataFrameCount`，16 | 由大小推算而來，所以兩者要一起調高 |
| `FrameReadTimeout` | `StandardFrameReadTimeout`，60 秒 | 會切斷閒置的對方，所以 ping 的間隔請在 30 秒以內 |

留給你的：`Text`、`Binary` 或 `Data` hook，以及 `Listen` 返回後關閉連線。

**每一項都可以改。** 想在標準 hook 上加東西，就把它留下來，在你的 hook 裡呼叫它：

```go
standardClose := config.Close
config.Close = func(frame *wlgows.Frame) {
	payload, _ := frame.GetClosePayload()
	log.Printf("peer closed: %+v", payload)
	standardClose(frame)
}
```

這些 hook 都有匯出，所以 `wlgows.NewListener(conn)` 可以只用其中幾個，例如
`Ping: conn.RFC6455PingHook`。

## 純 Listener 不會替你做的三件事

純 Listener 是 `wlgows.NewListener(conn)` 建立、沒有 hook 的 Listener。標準
Listener 的 hook 會替你做第二件事。第一件與第三件一律由你負責。

### 它不會關閉連線

不論是 close frame、error、超過上限的 message 或不認識的 opcode，它都只會返回，
由**你**來關閉。

RFC 6455 5.5.1 要求兩端在不同時間關閉。server MUST 在交換 close frame 後立刻關閉。
client SHOULD 等 server 先關。Listener 不知道自己是哪一端，所以關閉交給你。

client 要等的話，在交換 close frame 之後繼續讀取，並設定 deadline，直到讀取失敗再
關閉。在 TLS 連線上呼叫 `netConn.Close()` 也會結束 TLS session，這正是 7.1.1 要求的。

### 它不會寫出任何東西

不回 pong、不回 close、不做 echo。quick start 裡的每一個 `Send*` 都是你的。

所以**沒有 hook 的 Listener 什麼都不回應**。對方會一直等不到 pong；保留的 opcode
會被丟掉而繼續讀取，但 5.2 規定這時要讓連線失敗。

### 它沒有 ping pong 存活偵測

`Ping` 和 `Pong` hook 只告訴你收到了 frame。斷線的對方和安靜的對方看起來一樣，
所以你需要自己的計時器：

- 定期送出 ping：[`StartPingLoop`](./SENDING_README.zh-TW.md#定期送出-ping) 會替你做
- 收到**任何** pong 就重設 deadline，不要比對 payload
- deadline 到了還沒收到 pong，就當作對方已經斷線，關閉連線

在 `Pong` hook 中記錄時間，超過你的期限就關閉連線：

```go
var lastPong atomic.Value // time.Time
config.Pong = func(*wlgows.Frame) { lastPong.Store(time.Now()) }
```

不要比對 payload。RFC 6455 允許對方主動送 pong（5.5.3），也允許只回最新的那個
ping（5.5.3）。任何 pong 都證明對方還在。

`FrameReadTimeout` 處理不了這件事。它只在**時限內沒有讀完一整個 frame**時觸發，所以持續送
資料卻不理 ping 的對方仍然看起來正常。

## Configuration

`SetConfig` 在 lock 下複製整個 `ListenerConfig`，從任何 goroutine 或 hook 裡呼叫都
安全。設定裡沒有連線：`NewListener` 時就固定了。

每一項設定，以及沒設定時的行為：

| 項目 | 型別 | 零值 | |
|---|---|---|---|
| `PeerIsClient` | `bool` | 對方是 **server** | 對方是哪一端，決定 masking 的規則（5.1） |
| `MaxDataFramesSize` | `uint64` | 不限制 | 單一 message 所有 data frame 的 byte 上限（包含 header），每讀到一個 header 就檢查一次 |
| `MaxDataFrameCount` | `uint64` | 不限制 | 單一 message 最多可以由幾個 frame 組成 |
| `MaxFrameSize` | `uint64` | 不限制 | 單一 data frame 或保留 opcode frame 的 byte 上限（包含 header），讀到 header 時就檢查 |
| `FrameReadTimeout` | `time.Duration` | 不逾時 | 每個 frame 各自計時，每次讀取前重新設定 |
| `Ping` | `func(*Frame)` | 直接丟掉 | [5.5.2、5.5.3](#hooks)：回一個 pong，payload 原樣帶回 |
| `Pong` | `func(*Frame)` | 直接丟掉 | [5.5.3](#hooks)：不需要回應，所以設為 nil 就符合規範 |
| `Close` | `func(*Frame)` | 直接丟掉 | [5.5.1](#hooks)：回一個 close（這一端已送過就不用），然後 `PauseListen` |
| `Text` | `func(DataFrames)` | 直接丟掉 | 完整的 message，已經確認是 UTF-8 |
| `Binary` | `func(DataFrames)` | 直接丟掉 | 完整的 message，內容可以是任意 byte |
| `Data` | `func(*Frame)` | 改由 `Text`/`Binary` 組裝 | [每個 data frame 直接交給你](#hooks)，不保留任何東西 |
| `Unknown` | `func(*Frame)` | 直接丟掉 | [5.2](#hooks) 保留的 opcode：回 1002，然後停止 |

**它會取代全部設定。** 沒填的欄位會變成零值，所以只改一項時，要先把設定讀回來：

```go
config := listener.GetConfig() // 目前使用中的設定的複本
config.MaxDataFrameCount = 4000
listener.SetConfig(config)
```

hook 改設定也是這樣做。新設定從下一個 frame 開始生效。

沒有必填的項目，零值都能運作。但 `PeerIsClient` 一定要設對：它的零值代表對方是
server，所以沒設它的 server 會拒絕 client 的每一個 frame。

一定要設 `MaxDataFramesSize` 與 `MaxFrameSize`。10 byte 的 header 就能宣稱 10 GB
的 payload，沒有上限就會配置 10 GB 的記憶體。保留 opcode 的 frame 只受
`MaxFrameSize` 限制。

**`MaxDataFrameCount` 怎麼設。** 分段由對方決定，所以從你接受的最小 fragment 來算：

	MaxDataFrameCount = MaxDataFramesSize / 你預期的最小 fragment

10 MB、每段 4 KB 是 2560 個 frame，設 4000 還有餘裕。寧可設高。設太高影響不大，
因為 `MaxDataFramesSize` 已經限制了記憶體。設太低會拒絕合法的 message，而且查不出
原因。之所以需要它，是因為空的 fragment 只佔 2 byte 的上限，卻要一整個 `Frame` 的
記憶體。

**設了 `Data` 時，byte 上限要用整個 message 來算**，不是單一 frame。見
[Hooks](#hooks) 裡的 `Data`。

每一項的完整說明在它的 doc 裡：

```bash
go doc github.com/weilun-shrimp/wlgows/v7.ListenerConfig
```

## Hooks

hook 在 read loop 裡一次執行一個。hook 執行時 **loop 會停住**，不會讀任何 frame，
連 ping 也一樣。慢的工作請交給 goroutine 或 queue。hook 不會同時執行，所以 message
會依序抵達（5.4）。

hook 設為 nil，那種 frame 就會直接丟掉。

| hook | 收到什麼 | RFC 要求你做什麼 |
|---|---|---|
| `Ping` | 一個 frame | 5.5.2、5.5.3：MUST 回一個 pong，payload 原樣帶回；如果已經收到 close 就不用 |
| `Pong` | 一個 frame | 5.5.3：不需要回應 |
| `Close` | 一個 frame | 5.5.1：如果這一端還沒送過 close，MUST 回一個 close；之後關閉連線。`Frame.GetClosePayload` 會解出 status code 和 reason，兩者都已經檢查過。client 不能把 reason 顯示給使用者 |
| `Text` | 完整的 message | 不用做什麼：payload 已經確認是合法的 UTF-8（5.6、8.1） |
| `Binary` | 完整的 message | 不用做什麼：內容可以是任意 byte |
| `Data` | 一個 data frame | **使用時要小心。** 你要自己注意 FIN；text message 也要自己檢查 5.6 的 UTF-8，不合法就回 1007。只適合大到放不進記憶體的 message，其他情況請用 `Text` 或 `Binary` |
| `Unknown` | 一個 frame | 5.2 保留的 opcode，屬於 protocol error：回 1002，然後關閉連線 |

`Text` 和 `Binary` 收到組好的完整 message。夾在 fragment 之間的 control frame 會交給
自己的 hook，不會影響 message。

text message 交給 `Text` 前會檢查 UTF-8（5.6）。檢查的是**組好之後**的內容，因為
frame 可能切在字元中間。`Binary` 不檢查。

`Data` 取代組裝。每個 data frame 直接交給它，不保留，`Text` 和 `Binary` 都不會
執行。只在 message 大到放不進記憶體，或要轉送串流時使用。放得進記憶體的 message
請交給 `Text` 或 `Binary`，它們會處理其餘的事。

用 `Data` 時，FIN 與 UTF-8 都要自己檢查，因為單一 frame 無法檢查。continuation
frame 沒有型別，所以要用 `Listener.GetCurrentMsgOpcode` 確認 message 是不是 text。
上限仍然有效，空的 frame 也會送來。請在兩個 message 之間設定 `Data`。

把 message 直接寫進磁碟。不論多大，記憶體用量都不會增加：

```go
out, err := os.Create("./received.bin")
if err != nil {
	return err
}
defer out.Close()

listener := wlgows.NewListener(conn)
listener.SetConfig(wlgows.ListenerConfig{
	PeerIsClient:      true,
	MaxDataFramesSize: 2 * 1024 * 1024 * 1024, // 整個 message 一起計算，所以要用整個串流的大小
	MaxFrameSize:      64 * 1024,              // 單一 frame，讓每次讀取都很小
	FrameReadTimeout:  60 * time.Second,

	Data: func(frame *wlgows.Frame) {
		// 只接受 binary。text message 必須在組好的內容上檢查 5.6，所以要先確認
		// opcode。
		if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
			// 拒絕它：回 close 1003，或依照你的協定處理。
			listener.PauseListen(errors.New("peer streamed text"))
			return
		}
		if _, err := out.Write(frame.PayloadData); err != nil {
			listener.PauseListen(err) // 是你的磁碟出錯，不是對方的問題
			return
		}
		if frame.FIN { // 只有 FIN 能判斷 message 結束了
			log.Println("message complete")
		}
	},
	Ping: func(frame *wlgows.Frame) {
		conn.SendPong(frame.PayloadData) // 5.5.2、5.5.3：回一個 pong，payload 原樣帶回
	},
	Close: func(frame *wlgows.Frame) {
		payload, _ := frame.GetClosePayload()
		conn.SendClose(payload) // 5.5.1：回一個 close，然後停止
		listener.PauseListen(nil)
	},
	Unknown: conn.RFC6455UnknownHook(listener), // 5.2：保留的 opcode 要讓連線失敗
})

// 不管是讀取失敗、frame 違反規則，還是上面的 PauseListen，都會在這裡結束。
return listener.Listen()
```

這裡要注意 `MaxDataFramesSize`。它涵蓋整個 message，所以必須容納整個串流，這也
讓單一 frame 能用掉全部上限，所以也要設定 `MaxFrameSize`：它限制每個
frame，message 仍然可以很長。

[`stream_server`](./example/stream_server/main.go) 是完整範例：逐個 frame 接收檔案
並計算 hash。

**收到 close frame 之後就不要再讀。** RFC 6455 1.4（非規範性章節）說明收到 close
之後收到的 data 都應丟棄。
Listener 在 `Close` 之後會繼續讀，所以你的 `Close` hook 要呼叫 `PauseListen`，否則之後
的 message 仍會交給 `Text` 或 `Binary`。

### 不複製地讀取 text：`unsafe`

**只有在你完全清楚自己在做什麼時才使用。**
[傳送說明](./SENDING_README.zh-TW.md#更快更省記憶體用-unsafe-送出-string)裡的警告這裡同樣
適用。不確定的話，請用 `frames.String()`，只多一次複製。

wlgows 讀完後從不寫入 payload，而且每個 frame 有自己的 payload。所以單一 frame 的
message 可以不複製就轉成 string：

```go
config.Text = func(frames wlgows.DataFrames) {
	if len(frames) != 1 {
		handle(frames.String()) // 有分段：要組起來本來就得複製
		return
	}
	payload := frames[0].PayloadData
	handle(unsafe.String(unsafe.SliceData(payload), len(payload)))
}
```

1 MB 的 message，`frames.String()` 約 60 µs，配置 1 MB。`unsafe` 約 2 ns，不配置，
所以 message 只存一份，不是兩份。

- **只用於單一 frame 的 message。** 有分段的必須組起來，`frames.String()` 一次配置
  就組好。
- **之後絕對不要寫入 `payload`**，否則 string 也會變。Go 假設 string 不會變，用它當
  key 的 map 就會出錯。
- **這個 string 會讓整個 payload 留在記憶體。** 只想留大 message 的一小段時，請用
  `strings.Clone` 複製。
- 在 `Text` 執行之前，這些 byte 已經確認是合法的 UTF-8。

binary message 不需要 `unsafe`：`frames[0].PayloadData` 本來就是 `[]byte`。只要不寫入它。

## Errors

`PauseListen(nil)` 之後，如果沒有其他錯誤，`Listen` 回傳 `nil`。`PauseListen(err)`
會讓 `Listen` 回傳 `err`，所以你可以說明停止的原因，例如關機。以第一次 pause 為準，
之後的會被忽略。

pause 常伴隨讀取失敗，因為關閉連線才能叫醒卡住的讀取。這時兩個 error 會**合併**
回傳，`errors.Is` 兩個都找得到。所以 error 不是 nil，不一定是對方的錯。

`StandardClosePayloadFor` 不認得你自己的 error。除非你先轉換，否則它回傳 `nil`，
也就是下面的第 4 種。

其他 error 來自這個 package。`io.EOF` 不是正常結束：對方**沒有**送 close frame 就
斷開了 TCP 連線，7.4.1 稱為 1006。

這些 error 分成四種，各需要不同的處理。

### 1. 某個 frame 違反規則

對方違反了 RFC 6455。回一個 close frame，然後關閉連線。`StandardClosePayloadFor`
就是 7.4.1 的這張對照表：

| error | 回應的 status code |
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

**發生任何一個之後，都不要再讀。** 7.1.7 規定連線失敗之後不能再處理對方送來的
任何資料；其中有些情況下，被拒絕的 frame 只讀了一部分，下一次讀取會從它中間開始。
寫入仍然可以，所以 close frame 送得出去。

### 2. 連線斷了

`io.EOF`、`io.ErrUnexpectedEOF`、`os.ErrDeadlineExceeded`、`net.ErrClosed`、
connection reset。它們來自 socket，對方沒有違反規則，也沒有對象可送 close frame。
7.4.1 稱為 **1006** 並禁止送出，所以記錄下來並關閉。

讀取逾時是你自己的 `FrameReadTimeout`，不是對方的錯。socket 通常還能寫，所以你
*可以*先送 1000 或 1001。這由你決定，所以 `StandardClosePayloadFor` 回傳 `nil`。

### 3. Listener 使用方式錯誤

`ErrListenerConnIsNil`：Listener 不是用 `NewListener` 建立的，沒有連線。
`ErrListenerIsListening`：已有一個 `Listen` 在跑時又開始了第二個。兩者都在讀取前回傳。

**遇到這兩種不要關閉連線。** 可能有另一個 `Listen` 正常地在用它。這是程式碼的 bug，
請修掉。

### 4. 其他 error

自訂 `net.Conn`、TLS 層或 mock 回傳的一般 error。這個 package 無法判斷是誰的錯。

請當成第 2 種：關閉連線，什麼都不送。7.1.7 對送 close frame 只是 SHOULD，所以不送
並不違規，而送 1002 會冤枉可能沒做錯的對方。要送的話，1011 表示問題出在這一端。如果你知道自己的 error 代表什麼，請在
`StandardClosePayloadFor` **之前**自己轉換，它對這種 error 回傳 `nil`。

### 整理起來

`StandardClosePayloadFor` 只對第 1 種回傳 payload，其他回傳 `nil`。`nil` 只代表
「不送 close frame」。第 2、4 種仍要關閉連線，第 3 種則不能關閉：

```go
err := listener.Listen()
switch {
case err == nil: // PauseListen(nil)，不需要回應

case errors.Is(err, wlgows.ErrListenerConnIsNil),
	errors.Is(err, wlgows.ErrListenerIsListening):
	log.Printf("listener misuse: %v", err) // 第 3 種：不要動這條連線

default:
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		// 第 1 種：關閉之前，先用它回應
	}
	// 第 1、2、4 種都要關閉連線（7.1.7：不論哪一端都 MUST 關閉）。這兩個呼叫都由你來做。
}
```

`Reason` 是空的。想告訴對方原因，就在送出前自己填。

保留的 opcode（5.2）會交給 `Unknown` hook，不會交給 `Listen`。標準的 hook 會回 1002，
並以 `ErrReservedOpcode` 停止。用你自己的 hook 時，這兩件事都由你處理。hook 是 nil
時會直接丟掉，和其他 nil hook 一樣。

## 暫停與重新開始

`PauseListen(err)` 結束這次執行，`Listen` 回傳 `err`。傳 `nil` 就是不帶 error
地停止。從任何 goroutine 呼叫都安全，沒有在 listen 時它什麼都不做。

pause 在**兩個 frame 之間**生效，因為 loop 停在 `GetNextFrameHeader` 裡等資料。對方
一直不送資料時，讀取會卡住，只有關閉連線能讓它停下。

再次 `Listen` 會從停下的地方繼續。組到一半的 message 會保留，所以可以在中間改設定，
不會漏掉 frame。

## 不用 Listener 讀取

**不建議這麼做。只有在你有比 Listener 更好的理由時才使用。** Listener 會替你回應與
擋下 RFC 6455 要求讀取端處理的事。不用它，這些全都由你負責，漏掉任何一條規則都會
破壞協定。即使是大到放不進記憶體的 message 也不需要這麼做：`Data` hook 就能一次一個
frame 地串流處理。

讀一個 frame 要分兩步。`conn.GetNextFrameHeader()` 回傳只有 header、沒有 payload
的 frame，讓你在配置任何記憶體之前先判斷它的大小。接著 `conn.ReadFramePayload(frame)`
讀取 payload。兩步都要在 `conn.LockRead()` 之下進行，讓其他讀取者無法在中間插入。
message 由你自行組合：

```go
var frames wlgows.DataFrames
for {
	conn.LockRead()
	frame, err := conn.GetNextFrameHeader()
	if err == nil && frame.GetDeclaredPayloadSize() > 10*1024*1024 {
		err = errors.New("frame too large") // 還沒配置任何記憶體
	}
	if err == nil {
		err = conn.ReadFramePayload(frame)
	}
	conn.UnlockRead()
	if err != nil {
		return err
	}
	if !wlgows.IsDataOpcode(frame.Opcode) {
		continue // control frame：自己回應；保留 opcode 則依 5.2 讓連線失敗
	}
	frames = append(frames, frame)
	if frame.FIN {
		break
	}
}
```

這樣一來，Listener 做的每件事都由你負責。其中一部分有 helper 可以直接呼叫：

| 工作 | Helper |
|---|---|
| 用 pong 回應 ping（5.5.2） | `conn.RFC6455PingHook(frame)` |
| 依 error 選出 close code（7.4.1） | `wlgows.StandardClosePayloadFor(err)` |

其他都沒有 helper。`RFC6455CloseHook` 與 `RFC6455UnknownHook` 需要一個 Listener
才能停止。回應 close 由你負責，以下檢查也是：保留位元、mask 方向、保留的 opcode、control frame 規則、
fragment 順序、上限、text 的 UTF-8，以及 close payload。

`DataFrames` 只在你需要時才組合：

| 呼叫 | 回傳 |
|---|---|
| `frames.String()` | 串接後的 payload，型別為 `string` |
| `frames.Bytes()` | 串接後的 payload，是一份新的 `[]byte`，可自由使用 |
| `frames.ByteLen()` | 串接後的大小，單位為 **byte**，完全不配置記憶體 |

`String` 與 `Bytes` 都會串接所有 fragment，所以被切開的字元也能完整還原。`ByteLen` 算的是 byte
不是字元：`"中文字"` 是 9，不是 3。

只有第一個 frame 有 opcode，continuation frame 是 0：

```go
switch frames[0].Opcode {
case wlgows.OpcodeText:   // 0x1
case wlgows.OpcodeBinary: // 0x2
}
```

## 記憶體與 read buffer

frame 經由你交給 handshake 的 `*bufio.Reader` 讀取。`bufio.NewReader(netConn)` 每條
連線 4096 byte。任何大小都可以，小一點能省記憶體，只慢一點點：

```go
reader := bufio.NewReaderSize(netConn, 16) // 16 是下限：小於 16 會被 bufio 調整為 16
```

16 就夠了：最長的 frame header 是 14 byte（2 + 8 長度 + 4 mask），仍能一次讀完。
更大的 buffer 只是讓小 payload 一起讀進來。大 payload 不論 buffer 多大都要另外讀。

記憶體隨連線數增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 每條連線 | 16 B | 4096 B |
| 1,000 條連線 | 約 16 KB | 約 4 MB |
| 100,000 條連線 | 約 1.6 MB | 約 400 MB |

讀取次數隨連續到達的 frame 數增加：

| | 16 byte | 4096 byte（預設） |
|---|---|---|
| 一個 10 B 的 frame | 1 次讀取 | 1 次讀取 |
| 連續 100 個 10 B 的 frame（共 1000 B） | 約 63 次讀取 | 最少 1 次讀取 |
| 一個 1 MB 的 frame | payload 不經過 buffer，兩者相同 | payload 不經過 buffer，兩者相同 |

100 個 frame 那一列假設資料都已到達。對方慢時差距會縮小。

一整條閒置連線的用量，見
[最小的連線](./SENDING_README.zh-TW.md#最小的連線)。

## Concurrency

**只用一個 goroutine 讀取。** lock 能避免兩個讀取者混到 byte，但它們仍會各自拿到
零散的 frame。Listener 就是一個讀取者，所以它執行時不要自己讀取 frame。

`conn.Read` 來自內嵌的 `net.Conn`，會略過 lock。請透過 Listener，或
在 `LockRead` 之下用 `GetNextFrameHeader` 與 `ReadFramePayload` 讀取。
