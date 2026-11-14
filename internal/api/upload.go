package api

// UploadStart opens, or finds again, an upload sent in numbered parts: the
// file's SHA-256 and its size name it.
type UploadStart struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// UploadState is what labd holds of an upload. Parts are numbered from 1;
// every part is PartBytes long but the last. AtOnce is how many parts a
// client may send at the same time, 1 or 4.
type UploadState struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	PartBytes int64  `json:"part_bytes"`
	AtOnce    int    `json:"at_once"`
	Held      []int  `json:"held"`
}

// StoredArtifact is labd's answer once a file is in its artifact store.
type StoredArtifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
