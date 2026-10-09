[English](./LISTENER_README.md) · **繁體中文**

# Listener

`Listener` 會替你跑 read loop。它從連線讀取 frame、擋下 RFC 6455 不允許的 frame、
把分段的 message 組回來，再依照 opcode 呼叫你設定的 hook。

它只處理協定的**格式**，不替你做**決定**。RFC 要求接收端做的每一件事，都交給某個
hook 處理，Listener 本身不會做。

## Contents

- [Quick start](#quick-start) — 從頭到尾處理一條連線
- [它不會替你做的三件事](#它不會替你做的三件事) — [關閉](#它不會關閉連線) · [寫出](#它不會寫出任何東西) · [存活偵測](#它沒有-ping-pong-存活偵測)
- [Configuration](#configuration) — `SetConfig`、`GetConfig`，以及哪一項一定要設對
- [Hooks](#hooks) — 哪種 frame 會交給哪個 hook，以及 RFC 要求你做什麼 — [不複製地讀取 text：`unsafe`](#不複製地讀取-textunsafe)
- [Errors](#errors) — 四種 error，以及哪幾種要回 close frame — [1. frame 違反規則](#1-某個-frame-違反規則) · [2. 連線斷了](#2-連線斷了) · [3. 使用方式錯誤](#3-listener-使用方式錯誤) · [4. 其他 error](#4-其他-error) · [整理起來](#整理起來)
- [暫停與重新開始](#暫停與重新開始) — 結束一次執行，再重新開始

library 的其他部分（傳送、streaming、存活偵測、handshake、lock）請見
[main README](./README.zh-TW.md)。

## Quick start

`conn` 是一條已經完成 handshake 的 `*wlgows.Conn`。怎麼取得它，請見 main README 的
[`ServerHandShake`](./README.zh-TW.md#server)。下面這段程式從頭到尾處理一條連線，
也處理了 `Listen` 每一種可能的回傳結果。`Pong` 是唯一刻意不設的 hook：5.5.3 規定
收到 pong 時 MUST NOT 回應，而 hook 設為 nil 剛好就是不回應。

`SetConfig` 一次傳入完整的設定，什麼時候都可以呼叫：`Listen` 之前、兩次執行之間，
或在 hook 裡面都可以。

```go
func handleConn(conn *wlgows.Conn) {
	defer conn.Close() // 要由你關閉：Listener 不會關任何東西

	listener := wlgows.NewListener(conn)
	listener.SetConfig(wlgows.ListenerConfig{
		PeerIsClient:      true,             // 我們是 server，所以對方會 mask
		MaxDataFramesSize: 10 * 1024 * 1024, // 每個 message 最多 10 MB
		MaxDataFrameCount: 4000,             // 見 Configuration
		FrameReadTimeout:  60 * time.Second,

		Text: func(frames wlgows.DataFrames) {
			log.Printf("text: %s", frames.String())
		},
		Binary: func(frames wlgows.DataFrames) {
			log.Printf("binary: %d bytes", frames.ByteLen())
		},
		Ping: func(f *wlgows.Frame) {
			conn.SendPong(f.PayloadData) // 5.5.2：回一個 pong，payload 原樣帶回
		},
		Close: func(f *wlgows.Frame) {
			payload, _ := f.GetClosePayload() // Listener 已經檢查過了
			conn.SendClose(payload)           // 5.5.1：回一個 close，然後停止讀取
			listener.PauseListen(nil)         // nil：對方已經說明原因，Listen 回傳 nil
		},
		Unknown: func(f *wlgows.Frame) {
			// 5.2 保留的 opcode：回 close 1002，然後停止。
			conn.SendClose(&wlgows.ClosePayload{StatusCode: wlgows.CloseProtocolError})
			listener.PauseListen(errors.New("reserved opcode")) // Listen 會回傳這個 error
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

Listener 不寫出也不關閉任何東西，所以上面每一個 `Send*` 都是這個 function 自己呼叫
的：要送什麼由你決定，hook 只負責告訴你「什麼時候該送」。
`conn.NewStandardListener()` 會幫你設好同樣的 `Ping`、`Pong`、`Close` 與 `Unknown`
hook。`PauseListen` 是 Listener 在這裡唯一替你做的事：結束這次執行，讓這個
function 能夠返回，並把你給的原因一起帶回來。

## 它不會替你做的三件事

### 它不會關閉連線

收到 close frame、讀取失敗、payload 超過上限、遇到不認識的 opcode，它都不會關閉
連線，只會回報然後返回。`Listen` 回傳 error，代表**你**應該關閉連線，不代表它已經
關了。

這樣設計是有原因的。RFC 6455 7.1.1 對兩端的要求不同：雙方交換完 close frame 之後，
server MUST 立刻關閉 TCP 連線；client 則 SHOULD 等 server 先關，等了一段合理的時間
才能自己關。Listener 並不知道自己是哪一端。

### 它不會寫出任何東西

不回 pong、不回 close、不做 echo，它只負責讀。上面 quick start 裡的每一個 `Send*`
都是你的程式碼，寫在你的 hook 裡，或寫在 `Listen` 回傳之後。

所以**一個 hook 都沒設的 Listener，雖然完全符合規範，卻什麼都不會回應**。它會一直
安靜地讀，而對方則一直等著永遠不會來的 pong。

### 它沒有 ping pong 存活偵測

`Ping` 和 `Pong` hook 只會告訴你「收到了一個 frame」。要偵測對方是否已經斷線，得靠
你自己的計時器：

- 在你自己的 goroutine 裡定期送出 ping
- 收到**任何** pong 就重設 deadline，不要比對 payload
- deadline 到了還沒收到 pong，就當作對方已經斷線，關閉連線

不要比對 payload，是因為有兩種符合規範的行為會讓比對失準：RFC 6455 5.5.3 允許對方
主動送 pong，而 5.5.2 允許對方在有好幾個 ping 還沒回的時候，只回最新的那一個。

`FrameReadTimeout` 處理不了這件事。它只在**完全沒有 byte 進來**時才會觸發：一個
持續送資料、卻不理會 ping 的對方，對它來說還是正常的。

## Configuration

`SetConfig` 會在 lock 保護下複製整個 `ListenerConfig`，所以從任何 goroutine 呼叫都
安全，在 read loop 的 hook 裡呼叫也一樣。設定裡不包含連線：連線是 `NewListener` 時
就決定的，Listener 存在期間都不會變。

每一項設定，以及沒設定時的行為：

| 項目 | 型別 | 零值 | |
|---|---|---|---|
| `PeerIsClient` | `bool` | 對方是 **server** | 對方是哪一端，決定 masking 的規則（5.1） |
| `MaxDataFramesSize` | `uint64` | 不限制 | 單一 message 所有 data frame 的 byte 上限（包含 header），每讀到一個 header 就檢查一次 |
| `MaxDataFrameCount` | `uint64` | 不限制 | 單一 message 最多可以由幾個 frame 組成 |
| `FrameReadTimeout` | `time.Duration` | 不逾時 | 每個 frame 各自計時，每次讀取前重新設定 |
| `Ping` | `func(*Frame)` | 直接丟掉 | [5.5.2](#hooks)：回一個 pong，payload 原樣帶回 |
| `Pong` | `func(*Frame)` | 直接丟掉 | [5.5.3](#hooks)：MUST NOT 回應，所以設為 nil 就符合規範 |
| `Close` | `func(*Frame)` | 直接丟掉 | [5.5.1](#hooks)：回一個 close，然後 `PauseListen` |
| `Text` | `func(DataFrames)` | 直接丟掉 | 完整的 message，已經確認是 UTF-8 |
| `Binary` | `func(DataFrames)` | 直接丟掉 | 完整的 message，內容可以是任意 byte |
| `Data` | `func(*Frame)` | 改由 `Text`/`Binary` 組裝 | [每個 data frame 直接交給你](#hooks)，不保留任何東西 |
| `Unknown` | `func(*Frame)` | 直接丟掉 | [5.2](#hooks) 保留的 opcode：回 1002，然後停止 |

**每次都要給完整的設定。** 沒填的欄位會被設成零值，不會保留原本的值，所以只想改
一項的話，要先把目前的設定讀回來：

```go
config := listener.GetConfig() // 目前使用中的設定的複本
config.MaxDataFrameCount = 4000
listener.SetConfig(config)
```

hook 在執行中要改設定，也是用同樣的方式。正在處理的 frame 會沿用它開始時的設定，
所以新設定會從下一個 frame 開始生效，不會在處理到一半時改變。

沒有一定要設定的項目。每一項的零值都能正常運作，所以就算從沒呼叫過 `SetConfig`，
Listener 也能讀。不過 `PeerIsClient` 一定要設對：它的零值代表「對方是 server」，
如果 server 沒有設它，就會拒絕 client 送來的每一個 frame。

`MaxDataFramesSize` 則是最值得設定的一項。對方只要用 10 byte 的 header，就能
宣稱 payload 有 10 GB。沒有上限的話，在第一個 payload byte 抵達之前，就會先配置
10 GB 的記憶體。

**`MaxDataFrameCount` 怎麼設。** 對方怎麼分段不是你能控制的，所以從「你願意接受的
最小 fragment」來算：

	MaxDataFrameCount = MaxDataFramesSize / 你預期的最小 fragment

上限 10 MB、每段 4 KB，就是 2560 個 frame，所以設 4000 還有餘裕。寧可設高一點：設
太高只是稍微放寬記憶體上限，而 `MaxDataFramesSize` 本來就已經擋住了；設太低則
會拒絕對方完全合法的 message，而且你很難查出原因。這個設定存在的原因是：保留一個
frame 所佔的記憶體，比它從 byte 上限扣掉的還多。空的 fragment 只扣掉 2 byte 的
header，保留它卻要一整個 `Frame`。

**如果設了 `Data`，byte 上限要用整個串流的大小來算**，而不是單一 frame：上限仍然是
整個 message 一起計算的，只是 frame 之間不會保留任何東西。這樣做的代價，請見
[Hooks](#hooks) 裡 `Data` 的說明。

每一項設定的完整說明（包含適用哪些 frame，以及這些數字的理由）可以這樣查：

```bash
go doc github.com/weilun-shrimp/wlgows/v7.ListenerConfig
```

## Hooks

hook 由 read loop 依序呼叫，一次一個。hook 執行多久，**loop 就停多久**：這段期間
不會讀進任何 frame，等著回應的 ping 也一樣。所以比較慢的工作，請交給你自己的
goroutine 或 queue 處理。hook 不會被同時呼叫，因此 TCP 與 RFC 6455 5.4 保證的
message 順序可以完整保留下來。

hook 設為 nil，那種 frame 就會直接丟掉。

| hook | 收到什麼 | RFC 要求你做什麼 |
|---|---|---|
| `Ping` | 一個 frame | 5.5.2：MUST 回一個 pong，payload 原樣帶回；如果已經收到 close 就不用 |
| `Pong` | 一個 frame | 5.5.3：MUST NOT 回應 |
| `Close` | 一個 frame | 5.5.1：MUST 回一個 close，然後關閉連線。`Frame.GetClosePayload` 會解出 status code 和 reason，兩者都已經檢查過 |
| `Text` | 完整的 message | 不用做什麼：payload 已經確認是合法的 UTF-8（5.6、8.1） |
| `Binary` | 完整的 message | 不用做什麼：內容可以是任意 byte |
| `Data` | 一個 data frame | **使用時要小心。** 你要自己注意 FIN；text message 也要自己檢查 5.6 的 UTF-8，不合法就回 1007。只適合大到放不進記憶體的 message，其他情況請用 `Text` 或 `Binary` |
| `Unknown` | 一個 frame | 5.2 保留的 opcode，屬於 protocol error：回 1002，然後關閉連線 |

`Text` 和 `Binary` 收到的是完整的 message，所有 fragment 都已經組好，你不會看到分段。
如果兩個 fragment 之間夾了一個 control frame，它會交給自己的 hook，不會影響正在組裝
的 message。

text message 在交給 `Text` 之前，會依照 RFC 6455 5.6 檢查，而且是檢查**組好之後**
的內容：一個 frame 可能剛好切在某個字元中間，如果逐個 frame 檢查，就會把合法的
message 誤判成錯誤。`Binary` 完全不檢查，因為 5.6 規定 binary payload 可以是任意
byte。

設了 `Data`，就不會再組裝 message：每個 data frame 都直接交給它，不會保留，所以
`Text` 和 `Binary` 都不會被呼叫。這是整份設定裡最需要小心的一項。只有在你真的需要
frame 本身時才使用，例如大到放不進記憶體的傳輸，或要轉送出去的串流，而且要清楚
知道自己需要額外處理哪些事。放得進記憶體的 message，請交給 `Text` 或 `Binary`，
它們會幫你把該做的事做完。

你需要額外處理的是：FIN 要自己判斷；5.6 的 UTF-8 檢查也沒辦法只看一個 frame 就判斷
（frame 可能切在字元中間），所以不會有人幫你檢查。continuation frame 本身不帶
message 的型別，所以要用 `Listener.GetCurrentMsgOpcode` 確認這個 message 是不是
text，也就是需不需要你自己檢查。各種上限和 5.4 的規則都不變，所以交給你的，就是
組成這個 message 的那些 frame，空的也包含在內。請在兩個 message 之間設定 `Data`，
不要在 message 傳到一半時設定。

把一個 message 直接寫進磁碟，不管它有多大。因為 frame 之間不保留任何東西，記憶體
用量不會增加：

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
	FrameReadTimeout:  60 * time.Second,

	Data: func(f *wlgows.Frame) {
		// 只接受 binary。text message 必須在組好的內容上檢查 5.6，所以要先確認
		// opcode。
		if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
			// 拒絕它：回 close 1003，或依照你的協定處理。
			listener.PauseListen(errors.New("peer streamed text"))
			return
		}
		if _, err := out.Write(f.PayloadData); err != nil {
			listener.PauseListen(err) // 是你的磁碟出錯，不是對方的問題
			return
		}
		if f.FIN { // 只有 FIN 能判斷 message 結束了
			log.Println("message complete")
		}
	},
	Ping: func(f *wlgows.Frame) {
		conn.SendPong(f.PayloadData) // 5.5.2：回一個 pong，payload 原樣帶回
	},
	Close: func(f *wlgows.Frame) {
		payload, _ := f.GetClosePayload()
		conn.SendClose(payload) // 5.5.1：回一個 close，然後停止
		listener.PauseListen(nil)
	},
})

// 不管是讀取失敗、frame 違反規則，還是上面的 PauseListen，都會在這裡結束。
return listener.Listen()
```

這裡最需要考慮的是 `MaxDataFramesSize`。它是整個 message 一起計算的，所以必須
涵蓋整個串流；但它同時也是讀到 header 時，單一 frame 的上限。設得這麼大，等於允許
單一 frame 用掉全部的上限。「每個 frame 都限制得很緊，但 message 可以很長」是這種
做法唯一做不到的事。`Conn.GetNextFrame(max)` 做得到，因為它的上限是每次讀取各自
計算，不是整個 message 一起算。

[`stream_server`](./example/stream_server/main.go) 就是這個 hook 的完整範例：一個
frame 一個 frame 地接收檔案，同時計算 hash，上限也是用整個串流的大小設定的。

**收到 close frame 之後，就不要再讀了。** RFC 6455 5.5.1 規定，endpoint 收到 Close
之後 MUST NOT 再處理任何 data frame。Listener 不會幫你擋：它把 frame 交給 `Close`
之後會繼續讀。所以你的 `Close` hook 一定要呼叫 `PauseListen`，否則 close 之後才到的
message 還是會交給 `Text` 或 `Binary`。

### 不複製地讀取 text：`unsafe`

**只有在你完全清楚自己在做什麼時才使用。**
[傳送說明](./SENDING_README.zh-TW.md#更快更省記憶體用-unsafe-送出-string)裡的警告這裡同樣
適用。如果不確定，請用 `frames.String()`：代價只是多複製一次。

frame 讀進來之後，wlgows 從不寫入它的 payload，而且每個 frame 都有自己的 payload。
所以只有一個 frame 的 message，可以不複製地轉成 string：

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

以一則 1 MB 的 message 來說，`frames.String()` 約需 60 µs，配置 1 MB；`unsafe` 的
轉換約 2 ns，完全不配置。記憶體也省下一半：不用它的話，在 payload 被回收之前，這則
message 會同時存在兩份：payload 和 string。

- **只適用於只有一個 frame 的 message。** 有分段的 message 必須組起來，而
  `frames.String()` 已經只用一次配置就組好了。
- **轉換之後絕對不要再寫入 `payload`**，否則 string 會跟著改變。Go 假設 string
  永遠不會變，例如用它當 key 的 map 就會出錯。
- **這個 string 會讓整個 payload 一直留在記憶體裡。** 如果你只留下一大則 message 裡
  的一小段，整則 message 都會跟著留下來。要保留的話，先用 `strings.Clone` 複製那一段。
- 在 `Text` 執行之前，這些 byte 已經確認是合法的 UTF-8。

binary message 完全不需要 `unsafe`：`frames[0].PayloadData` 本來就是可以直接讀取、
不必複製的 `[]byte`，同樣不要寫入它。

## Errors

`Listen` 回傳 `nil`，代表是 `PauseListen(nil)` 結束了這次執行，而且沒有發生其他錯誤。
如果呼叫 `PauseListen` 時傳入 error，`Listen` 就會回傳那個 error。所以由你的程式
結束的執行，可以帶回結束的原因，例如收到關機訊號、你自己計時的 deadline 到了，或
是違反了這個 package 不知道的規則。以第一次 pause 為準；之後的 pause 會被忽略，不會
蓋掉真正結束的原因。

pause 和讀取失敗常常是同一件事，因為要叫醒卡住的讀取，就是關閉連線。這時兩個
error 會**合併**回傳，所以用 `errors.Is` 可以找到你傳入的原因，也可以找到 socket
本身的 error。如果 pause 傳的是 nil，就只會回傳讀取的 error。也因此，回傳的不是
nil，不一定代表是對方或協定出了問題。

這些是你自己的 error，`StandardClosePayloadFor` 不認得：它只處理協定本身的 error，
其他一律回傳 nil。所以你自己定義的 error 會被歸到下面的第 4 種，除非你先自己轉換。

其他的回傳值，都是這個 package 產生的 error。`io.EOF` 並不是正常的結束：它代表對方
**沒有**送 close frame，就直接斷開了 TCP 連線，也就是 7.4.1 說的 1006。

這些 error 分成四種，處理方式各不相同。重點就是分辨它屬於哪一種。

### 1. 某個 frame 違反規則

對方違反了 RFC 6455。回一個 close frame，然後關閉連線。`StandardClosePayloadFor`
就是 7.4.1 的這張對照表：

| error | 回應的 status code |
|---|---|
| `ErrReservedBitsSet` | 1002 |
| `ErrFrameNotMasked`、`ErrFrameMasked` | 1002 |
| `ErrControlFrameFragmented` | 1002 |
| `ErrControlFramePayloadTooLong` | 1002 |
| `ErrClosePayloadTooShort` | 1002 |
| `ErrPayloadLengthMSBSet` | 1002 |
| `ErrContinuationFrameWithoutMsg`、`ErrDataFrameDuringMsg` | 1002 |
| `ErrInvalidCloseStatusCode` | 1002 |
| `ErrInvalidUTF8` | 1007 |
| `ErrFrameByteLengthExceeded` | 1009 |
| `ErrDataFrameCountExceeded` | 1009 |

**只要發生其中任何一個，這條 stream 就不能再讀了。** 被拒絕的 frame 已經讀了一部分，
下一次讀取會從 frame 的中間開始，把 payload 當成 header 解析。絕對不要繼續讀。寫入的
方向不受影響，所以 close frame 還是送得出去，但不能再讀了。

### 2. 連線斷了

`io.EOF`、`io.ErrUnexpectedEOF`、`os.ErrDeadlineExceeded`、`net.ErrClosed`、connection reset。這些 error
來自 socket，不是這個 package 產生的，對方也沒有違反任何規則。連線已經斷了，也沒辦法
送 close frame。7.4.1 把這種情況稱為 **1006 abnormal closure**，而且禁止把 1006
送出去，因為這是 endpoint 留給自己的紀錄。所以記錄下來，然後關閉連線。

讀取逾時是你自己設定的 `FrameReadTimeout`，不是對方的錯。這時 socket 通常還能寫，
所以你*可以*先送 1000 或 1001。要送哪一個只有你能決定，所以
`StandardClosePayloadFor` 不會幫你決定。

### 3. Listener 使用方式錯誤

`ErrListenerConnIsNil` 代表這個 Listener 不是用 `NewListener` 建立的，所以沒有連線
可以讀。`ErrListenerIsListening` 代表第二個 `Listen` 和第一個
同時在跑。這兩種 error 都是在讀取任何 byte 之前就回傳了。

**遇到這兩種 error，不要關閉連線。** `ErrListenerIsListening` 代表另一個 goroutine
正在執行 `Listen`，關閉連線會中斷那個正常運作中的 session。這是程式碼的 bug，應該修掉，而不是
在執行時處理。

### 4. 其他 error

例如自訂的 `net.Conn`、TLS 層或 mock 回傳的一般 `errors.New("...")`，經由
`GetNextFrame` 傳上來。這個 package 無法判斷是誰的錯。

請當成第 2 種處理：關閉連線，不送任何東西。7.1.1 允許不送 close frame 直接關閉，
所以不送一定是合法的。反過來說，隨便回一個 1002，等於指責一個可能根本沒做錯事的
對方，而對方收到錯誤的 status code 也沒辦法補救。如果你知道自己的 error 代表什麼，
請在呼叫 `StandardClosePayloadFor` **之前**自己轉換，因為它只會回傳 `nil`。

### 整理起來

`StandardClosePayloadFor` 只會對第 1 種回傳 payload，其他都回傳 `nil`。`nil` 的意思
是*無法判斷是誰的錯*，不代表*連線沒問題*，也不代表*要關閉連線*。第 2、4 種要關閉，
第 3 種則絕對不能關閉：

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
	// 第 1、2 種都要關閉連線，而 7.1.1 規定了哪一端先關。這兩個呼叫都由你來做。
}
```

`Reason` 一律留空。要不要告訴對方發生了什麼事，這個 package 不替你決定；需要的話，
送出之前自己填上。

保留的 opcode 不會走到這裡。它不算 error：那個 frame 會原樣交給 `Unknown` hook，
要不要回 1002 由你處理。

## 暫停與重新開始

`PauseListen(err)` 會結束這次執行，而 `Listen` 會回傳 `err`。傳入 nil 代表停止，但
沒有要回報的原因。從其他 goroutine 呼叫是安全的；在 `Listen` 沒有執行時呼叫也是安全
的，這時它什麼都不做，傳入的 error 也會被忽略。

pause 只會在**兩個 frame 之間**生效，因為 loop 大部分時間都停在 `GetNextFrame` 裡
等資料。如果對方一直沒有送資料，`PauseListen` 會立刻返回，但讀取還是會卡住。只有
關閉連線才能讓它停下來，而這要由你來做。

重新開始時，會從上次停下的地方繼續。暫停時組到一半的 message 會保留下來，所以你可以
在中間更換設定，不會漏掉任何 frame。
