package remote

type Entry string

const (
	EntryLAN Entry = "lan"
	EntryTLS Entry = "tls"
	// EntryTailscale 是内嵌 tsnet 的 tailnet HTTPS 入口，与 EntryTLS 同为安全入口。
	EntryTailscale Entry = "tailscale"
)

// Secure 返回该入口是否提供传输层安全（HTTPS/WSS 安全上下文）。
func (e Entry) Secure() bool {
	return e == EntryTLS || e == EntryTailscale
}

// ClientIdentity 是传输层解析出的对端身份，仅用于展示与审计，不参与鉴权。
type ClientIdentity struct {
	LoginName string
	NodeName  string
}

type ClientInfo struct {
	DeviceID      string
	Label         string
	UserAgent     string
	Address       string
	Entry         Entry
	Authenticated bool
	// TailnetUser / TailnetNode 来自 tsnet 的 WhoIs，仅用于展示和审计，不参与鉴权。
	TailnetUser string
	TailnetNode string
}

type Conn interface {
	Info() ClientInfo
	MarkAuthenticated(deviceID, label string)
	Notify(method string, params any) error
	Close() error
}
