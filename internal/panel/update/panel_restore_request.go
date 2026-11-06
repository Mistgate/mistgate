package update

// PanelRestoreRequest is the metadata handed to the privileged helper. It contains no decryption key.
type PanelRestoreRequest struct {
	StageDir      string `json:"stage_dir"`
	Source        string `json:"source"`
	FileName      string `json:"file_name"`
	MadeByVersion string `json:"made_by_version"`
	RequestedUnix int64  `json:"requested_unix,omitempty"`
}
