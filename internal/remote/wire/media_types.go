package wire

// AttachmentsUploadParams 对应 attachments.upload 请求参数
type AttachmentsUploadParams struct {
	Name     string  `json:"name"`
	MimeType *string `json:"mimeType,omitempty"`
	Data     string  `json:"data"`
}

// AttachmentsUploadResult 对应 attachments.upload 成功结果
type AttachmentsUploadResult struct {
	AttachmentID string `json:"attachmentID"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	ByteCount    int    `json:"byteCount"`
	ExpiresAt    string `json:"expiresAt"`
}

// ThreadsMediaReadParams 对应 threads.media.read 请求参数
type ThreadsMediaReadParams struct {
	ThreadID  string `json:"threadID"`
	MessageID string `json:"messageID"`
	MediaID   string `json:"mediaID"`
	Variant   string `json:"variant"`
	Offset    *int64 `json:"offset,omitempty"`
	Length    *int   `json:"length,omitempty"`
}

// ThreadsMediaReadResult 对应 threads.media.read 成功结果
type ThreadsMediaReadResult struct {
	MimeType   string `json:"mimeType"`
	TotalBytes int64  `json:"totalBytes"`
	Offset     int64  `json:"offset"`
	NextOffset *int64 `json:"nextOffset,omitempty"`
	Truncated  bool   `json:"truncated"`
	Base64     string `json:"base64"`
}

const (
	MediaVariantThumb = "thumb"
	MediaVariantFull  = "full"
)
